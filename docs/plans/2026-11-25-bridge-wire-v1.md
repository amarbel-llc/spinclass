# Bridge wire, contract v1 (text for FDR 0048 "Wire")

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119. The
reference implementation is `internal/0/bridge` (standard library only); its
`testdata/vectors.json` is normative where this text is silent.

This file is the single home of the contract: clown's FDR 0048 links to it by
commit and does not restate it. When the bridge merges, this text becomes the
manpage `spinclass-bridge(7)` (principle 21: reference material is a manpage
shipped with the derivation, not a plan document); until then this file is the
source and the manpage is derived from it, not written beside it. Step 1 is the
`session` and `control` services; everything marked "extension" is not step 1.

## Socket and directory

The frontend listens on a unix stream socket named `bridge.sock` in the
directory named by `CLOWN_BRIDGE_DIR`; `CLOWN_BRIDGE_UNIT` names the session's
unit. The directory MUST be an absolute path to a real directory (not a symlink) owned by
the session's user with no group or other permission bits (`mode & 0077 == 0`).
The socket path (`<dir>/bridge.sock`) MUST NOT exceed 107 bytes; the frontend
checks this before it touches the disk, and when the path would be longer it
MUST NOT start the bridge and MUST say so, naming the length and the limit ("the
bridge stays off"). A socket file already at the path is replaced only when a
connect (1 second bound) is refused; a timeout or any other error leaves it
alone and the bridge stays off. On close the frontend removes the socket file
only if it is still the file it created. One socket serves every service of a
session.

## Hello

Every connection opens with one ASCII line, `BRIDGE/<major> <service>\n`, and
nothing before it. `<service>` matches `[a-z][a-z0-9-]{0,31}`. The line is at
most 64 bytes including the newline. A reader MUST read byte at a time, MUST
stop at the newline or after 64 bytes, and MUST NOT buffer further. A line with
no newline within 64 bytes, a longer line, CRLF, uppercase, a second space, or
any other deviation is malformed and answered `ERR error malformed hello`
before the connection is closed. Logs MUST NOT echo more than 32 bytes of a
malformed hello, and MUST escape non-printable bytes.

## Reply

The server answers with exactly one line: `OK\n`, or `ERR <code> <text>\n`
with `<code>` one of `version`, `unknown-service`, `refused`, `busy`, `error`.
`<text>` is one line (no CR or LF), at most 512 bytes: a sender replaces CR and
LF by a space and truncates over-long text at 512 bytes on a rune boundary; a
reader refuses text over 512 bytes. The server MUST send no service byte before
`OK\n`. After `OK\n` the connection is the service's byte stream in both
directions. The shim MUST read the reply byte at a time, up to the newline, so
service bytes that follow `OK\n` at once are not consumed; the reply is bounded
at 64+512 bytes. After an `ERR` the server closes the connection, in this order: it writes the
line, half-closes its write side (`CloseWrite`), and drains what the peer still
sends for at most 4096 bytes or 200 ms, so that a peer's unread hello cannot
turn the reply into a reset. Everything after `OK\n` is the service's: either
side may half-close; the frontend takes a peer's write-EOF on a data stream as
the end of the request, finishes its answer and closes, and a `control` stream
is not ended by a peer's write-EOF (see "Control service").

The text after `ERR <code>` is OPTIONAL human text. A reader MUST accept
`ERR <code>` with an empty text, with or without the separating space, MUST
NOT parse the text or branch on it (the code is the interface), and MUST show
or log it only within the 512-byte bound. A writer SHOULD send a text.

## Version

The version is the major only: `BRIDGE/<n>`, `n` a positive decimal integer of
at most 3 digits, no sign, no leading zero. This server supports exactly major
1. Any other well-formed major is answered
`ERR version server=1 shim=<n>: rebuild or restart the session so the shim and the frontend match`.
A malformed version is `ERR error malformed hello`. Version is judged before
the service label is looked up.

## Services and reserved labels

Reserved labels: `egress control session hook chat model keys status`. The
server decides which it serves. Any other well-formed label, and any reserved
label the server does not serve, is answered `ERR unknown-service <label>`.
A malformed label is `ERR error malformed hello`. A served label is answered
`OK\n` and the stream is that service's.

## Control service

`control` is the one stream a shim holds open for the life of the session. The
frontend uses it to learn the shim is alive (its loss means the session is
gone) and to push notices. At most one `control` stream per session is open; a
second is `ERR busy`.

Framing: after `OK\n` the stream is NDJSON in both directions, one JSON object
per line. A frame has a required string field `type`; a frame without it is
skipped. A reader MUST ignore a frame whose `type` it does not know (so a later
version can add types), MUST skip, not fail on, a line over 1 MiB, and MUST
NOT treat a skipped line as the end of the stream. The shim writes nothing
after its hello and never half-closes `control`: the end of the stream, in
either direction, IS the signal. The end of the stream from the frontend means
the bridge is down (the shim says so, see "Shim behaviour"); the frontend
treats a peer that is fully gone (not a peer's write-EOF alone) as the shim
having ended and frees the slot.

## Session service

`session` is MCP JSON-RPC 2.0, newline-delimited, over the stream, served by the
same server as stdio MCP. It lists and serves exactly five tools: `whoami`,
`list-handles`, `restart-this-session`, `cancel-restart` and
`close-this-session`. A tool outside the five is JSON-RPC -32602
(`unknown tool: <name>`) and is not listed. A call refused for want of a right
or because the caller is inside its unit is -32001 with text beginning
`refused: ` (see "Refusal text"); every other failure is a tool result with
`isError` (see the end of "Refusal texts"). The request never names the session
acted on, nor the caller: both are the listener's (see "Identity"). A bridged
call is CONFINED by construction: the classifier reads the origin the listener
put in the call's context, never the request bytes or the environment.

## Limits and timeouts

At most 64 open streams per session; the 65th hello is answered `ERR busy`. The
server MUST receive the whole hello line within 5 seconds of accept, else it
closes the connection without a reply. Hello and reply bounds are above.

## Refusal text

When a service refuses a call for want of a right, the text has the form
`refused: <what> needs the right "<right>"; <who> can grant it (<how>)`. Over
MCP the refusal is JSON-RPC error code -32001. When no policy exists at all the
text is `nothing crosses; the operator enables the bridge with <switch>`.

### Refusal texts

The `session` service's verbs, each refusal exactly as the frontend sends it
(JSON-RPC error -32001, the text prefixed `refused: `). A text names what is
true: where a verb is gated only because the caller is inside its unit, it has
no right a session could hold (the rights `observe`, `close`, `restart` and the
rest are rights of a holder over a child, which a caller inside its unit is not
allowed to use), and it says who may do the thing instead. No text carries a
value from the request.

| Verb | Over the bridge |
| --- | --- |
| `whoami` | Answers for the session the listener was created for, whatever the request names (see "Identity" below). Refused only when that session has no usable record. |
| `list-handles` | Plain listing is allowed. With `accept` (or arguments that do not parse): `refused: list-handles with accept has no right a session inside its unit can be granted; the operator runs `sc list-handles --accept` at a terminal outside the unit (a plain list-handles is allowed)` |
| `restart-this-session` | Allowed on the session itself. Without the trusted hand-off: `refused: restart-this-session needs the trusted [session-entry].restart hand-off; the operator sets it in the global sweatfile or one above the repo (a repo or worktree sweatfile is ignored)` |
| `cancel-restart` | With no `child`, cancels this session's own queued restart: allowed. Naming a child: `refused: cancel-restart naming another session needs the right "restart" on that session, which a session inside its unit may not use whatever it holds; a holder with the right, or the operator, runs `sc cancel-restart --child <session>` at a terminal outside the unit (cancel-restart with no child cancels this session's own)` |
| `close-this-session` | Allowed on the session itself. Without the trusted setting: `refused: close-this-session needs the trusted [permissions].allow-self-close = true; the operator sets it in the global sweatfile or one above the repo (a repo or worktree sweatfile is ignored)` |

The always-ask floor (`perms.AlwaysAsk`) lives only in the hook that runs inside
the unit, so a forced or widening act never rests on it over the bridge. The
verbs refuse the escalation arguments themselves, whatever their value:

| Verb and argument | Over the bridge |
| --- | --- |
| `restart-this-session` with `force`, `fresh_env` or any `posture` (`keep` included) | `refused: restart-this-session with force, fresh_env or posture is not available to a session inside its unit; a holder with the right "restart" on this session, or the operator at a terminal outside the unit, runs `sc restart-child-session <session>` with --force, --fresh-env or --posture (--force is always asked of the human)`. Nothing is recorded and no hand-off runs. |
| `close-this-session` with `force` (any value) | `refused: close-this-session takes no force argument and never discards work; the operator at a terminal outside the unit discards work with `sc close --force <session>`` |

The other bridged verbs have no escalation argument: `whoami` takes none,
`list-handles` has `accept` (refused above) and `cancel-restart` has `child`
(refused above); `reason` of a restart is free text that reaches the hand-off as
one argv element, never a shell.

Every POLICY refusal of `restart-this-session` and `close-this-session` (a rule
says no) is a refusal in this form: `refused: `, what is true, and who can lift
the rule (the operator in the global sweatfile, a holder with the right, or `sc`
at a terminal outside the unit), never a path the session itself has. It is
-32001 and logged and counted as `refused`. They are, with the text each has over
the bridge (the text off the bridge is unchanged):

| Rule | Over the bridge |
| --- | --- |
| a job is running (restart) | `refused: a job is still running in this session and a restart would kill it; let the job finish, or a holder with the right "restart" on this session, or the operator at a terminal outside the unit, runs `sc restart-child-session <session>` with --force to restart over it (--force is always asked of the human)` |
| a job is running (close) | `refused: a job is still running in this session; let it finish and ask again, or the operator at a terminal outside the unit ends the session with `sc close <session>`` |
| closing would orphan a session (close) | `refused: closing this session would leave a session with no accepted holder; a holder with the right "grant" on it, or the operator at a terminal outside the unit, grants a handle on it first with `sc grant-session-handle`, or reaps it` |
| unlanded work (close) | `refused: this session holds work that is not landed (uncommitted changes, or commits not merged), and close-this-session never discards work; land it and ask again, or the operator at a terminal outside the unit discards it with `sc close --force <session>`` |
| no restart hand-off, no `allow-self-close` | the two texts in the table above |
| a main-checkout (implicit) session, or a closed one | `refused: this session is a main-checkout (implicit) session or is closed, so there is nothing to <restart or close>; the operator at a terminal outside the unit can see its state with `sc list --closed`` (restart also has `refused: restart-this-session applies only to worktree sessions and this is a main-checkout (implicit) session; the operator at a terminal outside the unit can start a worktree session with `sc start``) |
| the recorded principal is not one the hand-off can name | `refused: restart-this-session cannot restart this session because its recorded principal is not one the restart hand-off can name; the operator at a terminal outside the unit can inspect it with `sc list`` |
| posture rules (reached through `restart-child-session`; a bridged `restart-this-session` is refused earlier for any `posture`) | each is `refused: ` with the rule and `the operator at a terminal outside the unit` or the trusted setting the operator sets; a session widening itself: `refused: a session may not widen its own permissions; a holder with the right "restart" on this session, or the operator at a terminal outside the unit, can restart it into the attended posture` |

A verb with no named right that a session inside its unit could hold (every
other gated verb, should one reach a bridged stream) is refused with the generic
text `refused: <tool> is not available to a session inside its unit; tell the
session's holder or the operator, who run `<command>` at a terminal outside the
unit`. A session inside its unit has no channel to ask anyone, so the text
names who runs the command, not whom to ask.

An error is not a rule: a hand-off that will not exec, an unreadable sweatfile or
a failed write stays a tool result with `isError`, logged and counted as `error`.
Arguments that do not parse likewise. A call to a tool outside the five
(`session-health` and `update-this-session-description` among them: a listed
verb that can only refuse is noise) is -32602, `unknown tool: <name>`.

### Identity

For a call that arrived on the bridge, the session acted on is the listener's
session key and nothing else: not the environment's principal, not the
`principals/` mapping file, and not any argument. `whoami`, `list-handles`,
`restart-this-session`, `cancel-restart` (own form) and `close-this-session`
read the frontend-held record of that key (never the index; the record's own
key must be it) and act as the principal that record carries. When the key
resolves to no live record, or the record carries no principal yet, the call is
refused (-32001) and never falls back to the environment's principal:

- `refused: this bridge's session has no live frontend record, so there is no session to act on`
- `refused: this bridge's session record carries no principal yet (the SessionStart stamp is not there), so there is no session to act as`

The frontend does not listen at all unless the principal it serves resolves,
through the frontend's own mapping, to the key the listener carries; otherwise it
stays off and the failure is logged and counted.

## Admission: who may connect

The listener admits a connection only when the peer's cgroup (read from its
pidfd, `SO_PEERPIDFD`, never from a pid number) is EXACTLY the unit's
`ControlGroup`: strict equality, no descendant, no prefix, no suffix match
(empty, `/` and a trailing slash are refused, and a peer with no pidfd is
refused, never admitted by a weaker check). The exact check (strict equality,
no descendants) is clown's `pkgs/peercheck.CheckPeerExact`, merged at clown
0265f77. The listener holds no checker of its own: with none wired it refuses
to start and says so.

The expected cgroup is read from the file named by `CLOWN_BRIDGE_CGROUP_FILE`,
on every connection, never cached. The file MUST be a regular file (not a link,
not a FIFO) owned by the session's user, mode 0600, outside the bridge
directory, and its content is exactly the cgroup path with no trailing newline.
Until the file is valid the listener admits nobody, and the answer to a peer it
cannot yet admit (the file missing, empty, unreadable or not as above, or a
peer outside the cgroup) is `ERR refused <text>` and nothing else: the shim
retries `refused` and a different code would end its retry. The text names the
reason in plain words and carries nothing from the peer.

When clown drops the bridge after the listener started, it removes `bridge.sock`
and `bridge.cgroup` and logs it. The listener MUST notice either file gone
after it was once valid, close, and say so (it checks on a short timer): a
socket that is not the file it created, or a cgroup file that was valid and is
gone, is a dead bridge, not a closed door to wait at. A cgroup file that was
never written is not death; it is `refused`, until it appears.

## Launcher obligations (clown)

These are what clown MUST do for the listener's checks to mean anything:

1. Create the directory (`<runtime>/clown/launch/<key>/bridge/`) mode 0700, owned
   by the session's user, absolute, not a link, and start from it clean: remove
   any stale `bridge.sock`, `bridge.cgroup` and `bridge.cgroup.tmp` before the
   frontend starts. Refuse a link or a foreign-owned directory.
2. Bind the directory into the unit READ-ONLY (`BindReadOnlyPaths=`; the
   reference test is `TestTheBridgeDirIsBoundReadOnlyAndNeverWritable` in clown's
   agentscope). Nothing writable (no `BindPaths=`, no `ReadWritePaths=`) may
   cover the directory or any directory above it that the unit can reach, or the
   unit could replace the socket.
3. Write the cgroup file OUTSIDE the bind (`<runtime>/clown/launch/<key>/bridge.cgroup`),
   mode 0600, content exactly the unit's `ControlGroup` path with no trailing
   newline, via `<file>.tmp` and a rename (a link in the way is replaced, not
   followed), as soon as systemd reports the unit active with its main process
   settled and BEFORE the provider starts. Refuse to write a value that does not
   end in `/clown-agent.slice/<unit>`. The file is written once per unit.
4. Set `CLOWN_BRIDGE_DIR`, `CLOWN_BRIDGE_UNIT` (`<name>.service`) and
   `CLOWN_BRIDGE_CGROUP_FILE` on the serve process only, and remove them from
   clown's own environment after routing them. When the bridge is off, whatever
   the reason (the operator switch, an unconfined session, any refusal that
   dropped it), all three are set to the EMPTY string, present and empty, not
   left unset. The frontend treats empty exactly as unset.
5. Wait up to 10 s after starting serve for `bridge.sock` (a socket, not a
   file). No socket means the frontend's own gate is off: clown binds nothing,
   sets no `CLOWN_BRIDGE_SOCKET` and advertises no `bridge-v1`; the launch goes
   on.

Inside the unit: `CLOWN_BRIDGE_SOCKET` is the socket's path (the same path as
outside), the shim's flag is `--bridge SOCK`, and clown advertises the result
capability `bridge-v1` only once the cgroup file was written. A view unit refuses
a bridge in code.

## Shim behaviour

The shim (clown's) acts per reply, by code only:

| Reply | On `control` | On a data stream |
| --- | --- | --- |
| `ERR version` | the bridge is down | the bridge is down |
| `ERR busy`, `ERR refused` | retried for a 15 s window (100 ms, doubling to 1 s); the first connect also tries three times, 200 ms apart, on an unreachable or busy bridge | said to that client; the stream fails, the bridge stays up |
| `ERR unknown-service`, `ERR error` | said to that client; final, the bridge stays up | said to that client; the bridge stays up |
| connect failed, EOF, no reply in time | retried as above | the call fails; with `control` held only that connection fails, without it the shim marks the bridge down |

A failed data dial fails only that connection while `control` is held. The end
of `control` is the bridge being down.

## Failure text

When the shim cannot reach the frontend (no socket, refused connect, closed
stream, no reply within the timeout) it MUST fail the call at once with
`clown: the bridge is down (<reason>); no tool or model call can leave this session; the session's frontend is gone or restarting`
and MUST NOT retry silently or fall back to a direct path. (The retries above
are the shim's connect attempts before the bridge is declared down, not a
fallback.)

## Egress (extension, NOT step 1, not built)

Provisional and unbuilt, recorded so that step 1 does not close the door: the
frontend stays the one door (accept, peer check, hello, label, policy, the open
logged) and hands the accepted connection to the service's owner (clown's
outer process) over a frontend-side unix socket by `SCM_RIGHTS`. The peer
credentials of the passed connection stay the original connector's, so clown's
proxy keeps its own exact unit-cgroup check and no peer check is loosened
(the credentials staying the connector's is expected behaviour, not yet tested
here). Because the frontend no longer sees the bytes or the close, the proxy
reports a close frame `{id, bytes, outcome}` back on the passing channel; the
frontend keeps the stream slot and the `open` event and frees the slot on the
close frame, or on a lease timeout when the proxy dies without one, so the
64-stream cap and the one observed event per connection still hold. The hand-off
socket needs its own exact peer check, a per-session path outside the unit's
binds, and a rule for an absent owner (`ERR error`, never a held fd).

## Not verified (to be observed, not asserted)

- Whether a relaunch of the session (an in-place restart) gives a new unit
  name, a new cgroup and a new frontend process. Code-read by clown's owner:
  one unit name and one serve per clown process, so a restart is expected to be
  a new unit and a new serve. NOT observed. The first live seam logs the serve's
  pid and `CLOWN_BRIDGE_UNIT` before and after one in-place restart.
- How long serve's stdio from clown lives. Not checked.

## Written disagreements (clown round 1, recorded)

- A listener whose gate is off while clown waits 10 s for the socket (B-M3): a
  rollout-order fact, not a defect: the operator switch and the frontend gate are
  set together; clown's log line names `[bridge] enable`.
- The down-message wording (B-L5) is the vector's and this contract's: clown
  does not change it alone. A wording that names a local cause is a new vector
  first.
- A counter for refused connections on clown's side (B-M6) is not built by
  clown; the refusal is logged there, rate limited, and counted by the frontend
  as `spinclass.bridge.unknown.refused` (a peer refused before its hello has named
no service).

## Crossing log

The frontend MUST keep a log with one NDJSON line per crossing, at
`<state>/spinclass/bridge/<principal>.ndjson` (the directory mode 0700, the file
0600, created by the frontend; the session's unit has no access to it). A line
has the fields `ts` (RFC 3339 with nanoseconds, UTC), `session`, `service`,
`verb`, `outcome`, `duration_ms`, `req_bytes`, `resp_bytes`, `arg_names`,
`other_arg_names` and `args_sha256`, and MAY have `fields` and `truncated`.
`outcome` is one of `ok`, `refused`, `error`, `busy`, `hello-timeout`,
`version`; a hello answered `ERR unknown-service` is logged as `refused` and
one answered `ERR version` as `version` (its own outcome: it means the shim
and the frontend were built apart). `service` is one
of the reserved labels or `unknown`. A call on the `session` service is one
line whose `verb` is the tool name (`unknown` for a tool the service does not
list) and whose outcome follows the JSON-RPC result: `ok`, `refused` when the
error code is -32001, `error` otherwise. A hello that did not become a stream
is one line with verb `hello`; a served `control` stream is one line with verb
`stream`.

Redaction (normative): argument VALUES MUST NOT be logged, and neither may an
argument NAME a verb does not declare. A line carries `arg_names`, the names
sorted (an empty list for none) of the arguments the verb's input schema
declares, `other_arg_names`, the number of other names the call carried (a
request cannot write its own text into the log through an argument name), and
`args_sha256`, the first 16 hex characters of the HMAC-SHA256 of the arguments'
canonical JSON (keys sorted, no insignificant whitespace; the raw bytes when the
arguments are not valid JSON) under a random key generated when the log opens
and never written anywhere (so the digest compares calls within one frontend
process and cannot be tested against a guessed argument by anyone who only has
the file). A line is at most 512 bytes including the newline: a longer one
loses its `fields`, then its `arg_names` (they stay counted), then the tail of
its `session`, and carries `"truncated": true`. A per-verb allow-list (`SafeFields`) may add
`fields`: a map of argument name to value for the names `session`, `branch` and
`target` only, each logged only if it is a string of at most 128 characters
matching `[A-Za-z0-9._/-]+`, otherwise dropped. Nothing from free text
(descriptions, messages, commands, reasons) is ever logged, and no attacker
text reaches `service` or `verb`.

Metrics: besides the per-stream counter and timer
(`spinclass.bridge.<service>.<outcome>`, `.duration_ms`), each `tools/call` on
the `session` service is counted and timed as
`spinclass.bridge.session.tool.<tool>.<outcome>` and
`spinclass.bridge.session.tool.<tool>.duration_ms`. `<tool>` is one of the five
tool names with `-` as `_`, or `unknown` for any other name (no text from the
request reaches a metric name); `<outcome>` is `ok`, `refused` (JSON-RPC error
-32001) or `error`, as in the log. Emission is best effort and bounded: a burst
that finds every slot taken drops the metric.

The log directory MUST be a real directory (not a link) owned by the frontend's
user, tightened to 0700 when it is not; the file is opened without following a
link (`O_NOFOLLOW`, `O_NONBLOCK`) and MUST be a regular file of that user, else
the write fails (counted, not delivered to the crossing), so a link or a FIFO
put where the log goes neither redirects nor blocks a crossing. The frontend's
own log lines about the listener (refused peers, refused hellos, panics) are
rate limited, one line per kind per 10 s, the next one carrying how many were
suppressed.

Retention: when a write would take the file past 4 MiB it is renamed to
`<principal>.ndjson.1` (older generations shift up; at most three are kept, `.1`
to `.3`), and log files older than 14 days are deleted when the log opens. A
failed log write MUST NOT fail or delay a crossing; it is counted and logged at
most once a minute.

## Vectors file and its digest

`internal/0/bridge/testdata/vectors.json` is a JSON array of cases with an `op`:
`hello` (stream bytes in `input`; expect the parse result, the reply line, and
`accept`; `rest` is what must stay unread), `reply` (stream bytes; expect
`ok`, `code`, `text`, and `rest`), `format-err` (code and text in; expect the
reply line), `socket-path` (`dir` or `dir_gen` in; expect `path`/`path_gen`
and, when given, `path_len`, or `path_err` of `too-long` or `empty`),
`down-message` (`reason` in; expect `message`) and `constants` (the values both
sides share: the socket name, the three variable names, the byte limits, the
hello timeout in ms, the major, the version hint, the reserved labels and the
reply codes; a side's own constants MUST equal them). `parse_err` is
`malformed` or `too-long`. Any string may instead be
given as a `*_gen` object `{prefix, repeat, n, suffix}`. `served` defaults to
all eight reserved labels. Both implementations MUST pass every case.
`vectors.json.sha256` holds `sha256sum` output; a vendored copy MUST match it,
and a change to the file MUST change the digest and bump this text. The file
has 82 array entries (a test run reports more than that, one per subtest and
helper case: count entries, not test lines).
