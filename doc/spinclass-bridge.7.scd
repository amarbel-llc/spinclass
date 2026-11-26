spinclass-bridge(7) "spinclass" "Spinclass Reference"

# NAME

spinclass-bridge - the per-session bridge: a confined session's one door out to
spinclass's own services (contract v1)

# SYNOPSIS

None: this page describes concepts and a wire contract. The operator's one
switch is the sweatfile key *[confinement] bridge* (see *spinclass-sweatfile*(5)).

# DESCRIPTION

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119.

A confined session runs in a unit that cannot reach the frontend's files or
sockets. The bridge is the one narrow way a session inside its unit reaches
spinclass's own services: ONE listener per session, inside that session's
*spinclass serve* process, on ONE unix stream socket named *bridge.sock*, in a
directory clown binds read-only into the unit. Clown's shim in the unit connects
to it; the frontend decides who the caller is from the connection, never from
what the caller says.

This page is the single home of the normative contract (principle 21: reference
material is a manpage shipped with the derivation). The reference
implementation is the Go package *internal/0/bridge* (standard library only);
its *testdata/vectors.json* and *testdata/admission-vectors.json* are normative
where this text is silent, and clown vendors both from there. Step 1 is the
*session* and *control* services; everything marked "extension" is not step 1.
The design history and the reasoning behind the choices are in
_docs/plans/2026-11-25-bridge-wire-v1.md_ (FDR 0048 is clown's record).

The bridge is OFF by default. With the switch off nothing is bound, nothing
listens and every behaviour of spinclass is what it was before the bridge.

# THE ONE SWITCH

The bridge has exactly ONE operator switch: the launch spec's field *bridge*,
which the frontend writes (*internal/foxtrot/spawn/confine.go*, type
*BridgeMode*) from the trusted-only knob *[confinement] bridge* with the values
*"off"*, *"offered"* and *"required"*. The default is off; the last trusted layer
that sets it wins; a repo or worktree layer is ignored and logged; a value
spinclass does not know refuses the spawn and *sc validate* reports it.

The field's values:

- Absent or empty: the bridge is OFF. Nothing is bound, all three environment
  variables below are set to the empty string, and serve does nothing. The knob
  value *"off"* is the same OFF and the field is omitted.
- *"offered"*: the bridge is ON. The launch goes on without a bridge when one
  cannot be set up, and clown's result says *bridge: off: <reason>* (one space
  after the colon). The result's *bridge* field is exactly one of: absent (the
  bridge was not asked for, or a clown that predates the field), *recorded*
  (set up, the cgroup file written), or *off: <reason>*. Clown no longer writes
  *on*.
- *"required"*: the bridge is ON, and clown refuses the launch when it cannot be
  set up, and when the shim's *control* stream is not held.

Any other value is refused by both sides. Making *"required"* the default is the
operator's flip of the knob, not a code change. The frontend reads no sweatfile
to decide whether to listen: *serve* listens exactly when clown exported the
three variables non-empty, which clown does only when the field asked for the
bridge. The earlier *[bridge] enable* stanza and clown's *CLOWN\_BRIDGE\_V1* are
both dropped: two switches meant two things to flip and a state where one was on
and the other off. Clown's result field says *recorded*, not *on*: that the
bridge was set up and its cgroup file written is clown's to say; the live state
("listening", "dead", "asked for, not running") is the frontend's to show (see
OBSERVABILITY).

The variables clown sets on the serve process only:

- *CLOWN\_BRIDGE\_DIR*: the absolute path of the bridge directory.
- *CLOWN\_BRIDGE\_UNIT*: the session's unit, *<name>.service*.
- *CLOWN\_BRIDGE\_CGROUP\_FILE*: the path of the cgroup file (see ADMISSION).

When the bridge is off, whatever the reason (the switch, an unconfined session,
any refusal that dropped it), all three are set to the EMPTY string, present and
empty, not left unset; the frontend treats empty exactly as unset. Inside the
unit, *CLOWN\_BRIDGE\_SOCKET* is the socket's path (the same path as outside),
the shim's flag is *--bridge SOCK*, and clown advertises the result capability
*bridge-v1* only once the cgroup file was written. A view unit refuses a bridge
in code.

# START ORDER

The bridge is brought up in this order. Who creates what, and what each waits
for, is the contract: a launcher or a frontend that changes it deadlocks (the
unit record needs the provider, the provider needs the shim's *control*, and
*control* must not need the record).

. Clown prepares the bridge directory and the three variables and starts serve.
. Serve binds at once: directory, lock, socket, the exact peer checker. It reads
  no principal mapping and no unit record. It writes its state file and logs
  *serve.bridge listening*.
. Clown waits up to 10 s for *bridge.sock*, then starts the unit.
. Clown writes *bridge.cgroup* when systemd shows the unit's main process
  settled, before the provider starts.
. The shim dials *control* (retry window 15 s on *ERR refused*, *busy* or an
  unreachable socket). The listener admits it on IDENTITY ALONE: the same user,
  the cgroup file read fresh, the exact peer check, and the file's value ending
  in */clown-agent.slice/<CLOWN\_BRIDGE\_UNIT>* (the unit comes from the
  environment; no unit record is read). Until the file is valid the answer is
  *ERR refused*.
. The shim reports ready with one line, *bridge-listen: ready control* when it
  holds *control* or *bridge-listen: ready down* when it does not; clown starts
  the provider. After the 15 s window without *control* the shim is marked
  unusable (its failure text is the "unusable" form below). With *required* and
  *control* not held the provider is never started (clown's *ShimExitRefused*);
  with *offered* it starts and the shim says the text on each call.
. Clown writes the unit record (its result file) once the provider has started.
. The spawn's hello writes the principal mapping from the unit record. The
  session verbs are ready.

Readiness gates ONLY the *session* service, per call, and only for a peer the
exact check already admitted (a foreign peer learns nothing about the session's
state). A *session* hello before readiness is answered
*ERR refused refused: the bridge is not ready yet: <condition>* (one line) and
the connection closes; the listener lives and the shim's retry applies. The
conditions:

- the frontend's principal mapping for the serve's principal does not exist yet,
  or cannot be used yet: not ready;
- that mapping names ANOTHER session than the one the listener carries: HARD
  FAILURE at any time;
- a unit record written BEFORE this serve started is the previous launch's and
  is ignored (a relaunch is a new clown process, a new unit name and a new
  serve); a missing record is not a failure, since the cgroup file admits a
  peer;
- a unit record written AFTER this serve started that names another unit than
  *CLOWN\_BRIDGE\_UNIT*: HARD FAILURE.

A hard failure refuses the call (*ERR refused refused: the bridge for this
session has stopped; the serve log says why*), ends the listener, marks the
state file down and wakes the holders. The listener also asks on its 2 second
tick, so a hard failure ends it without a connection.

# THE WIRE

## Socket and directory

The frontend listens on a unix stream socket named *bridge.sock* in the
directory named by *CLOWN\_BRIDGE\_DIR*. The directory MUST be an absolute path
to a real directory (not a symlink) owned by the session's user with no group or
other permission bits. The socket path (*<dir>/bridge.sock*) MUST NOT exceed 107
bytes; the frontend checks this before it touches the disk, and when the path
would be longer it MUST NOT start the bridge and MUST say so, naming the length
and the limit ("the bridge stays off"). A socket file already at the path is
replaced only when a connect (1 second bound) is refused; a timeout or any other
error leaves it alone and the bridge stays off. The socket is created with mode
0600 from the bind, is checked after it is bound, and a lock file in the
directory serialises clearing and binding. On close the frontend removes the
socket file only if it is still the file it created. One socket serves every
service of a session.

## Hello

Every connection opens with one ASCII line, *BRIDGE/<major> <service>\\n*, and
nothing before it. *<service>* matches *[a-z][a-z0-9-]{0,31}*. The line is at
most 64 bytes including the newline. A reader MUST read byte at a time, MUST
stop at the newline or after 64 bytes, and MUST NOT buffer further. A line with
no newline within 64 bytes, a longer line, CRLF, uppercase, a second space, or
any other deviation is malformed and answered *ERR error malformed hello* before
the connection is closed. Logs MUST NOT echo more than 32 bytes of a malformed
hello, and MUST escape non-printable bytes.

## Reply

The server answers with exactly one line: *OK\\n*, or *ERR <code> <text>\\n* with
*<code>* one of *version*, *unknown-service*, *refused*, *busy*, *error*.
*<text>* is one line (no CR or LF), at most 512 bytes: a sender replaces CR and
LF by a space and truncates over-long text at 512 bytes on a rune boundary; a
reader refuses text over 512 bytes. The server MUST send no service byte before
*OK\\n*. After *OK\\n* the connection is the service's byte stream in both
directions. The shim MUST read the reply byte at a time, up to the newline, so
service bytes that follow *OK\\n* at once are not consumed; the reply is bounded
at 64+512 bytes.

After an *ERR* the server closes the connection, in this order: it writes the
line, half-closes its write side, and drains what the peer still sends for at
most 4096 bytes or 200 ms, so that a peer's unread hello cannot turn the reply
into a reset. Everything after *OK\\n* is the service's: either side may
half-close; the frontend takes a peer's write-EOF on a data stream as the end of
the request, finishes its answer and closes, and a *control* stream is not ended
by a peer's write-EOF.

The text after *ERR <code>* is OPTIONAL human text. A reader MUST accept
*ERR <code>* with an empty text, with or without the separating space, MUST NOT
parse the text or branch on it (the code is the interface), and MUST show or log
it only within the 512-byte bound. A writer SHOULD send a text.

## Version

The version is the major only: *BRIDGE/<n>*, *n* a positive decimal integer of
at most 3 digits, no sign, no leading zero. This server supports exactly major 1.
Any other well-formed major is answered *ERR version server=1 shim=<n>: rebuild
or restart the session so the shim and the frontend match*. A malformed version
is *ERR error malformed hello*. The version is judged before the service label
is looked up.

## Services and reserved labels

Reserved labels: *egress control session hook chat model keys status*. The
server decides which it serves; step 1 serves *control* and *session*. Any other
well-formed label, and any reserved label the server does not serve, is answered
*ERR unknown-service <label>*. A malformed label is *ERR error malformed hello*.
A served label is answered *OK\\n* and the stream is that service's.

## Limits and timeouts

- At most 64 open streams per session; the 65th hello is answered *ERR busy*.
- At most 80 connections (the 64 streams plus 16) are held at once, counted from
  accept and before any hello; one more is answered *ERR busy* at once.
- The server MUST receive the whole hello line within 5 seconds of accept, else
  it closes the connection without a reply.
- A temporary accept error is retried with a pause that doubles from 5 ms to 1 s.
- A reply is written under a short deadline (2 s; 100 ms for a busy answer), so
  a peer that does not read cannot hold the listener.
- Closing the listener waits at most 5 s for the connection handlers.

# SERVICES

## control

*control* is the one stream a shim holds open for the life of the session. The
frontend uses it to learn the shim is alive (its loss means the session is gone)
and to push notices. At most one *control* stream per session is open; a second
is *ERR busy*.

Framing: after *OK\\n* the stream is NDJSON in both directions, one JSON object
per line. A frame has a required string field *type*; a frame without it is
skipped. A reader MUST ignore a frame whose *type* it does not know (so a later
version can add types), MUST skip, not fail on, a line over 1 MiB, and MUST NOT
treat a skipped line as the end of the stream. The shim writes nothing after its
hello and never half-closes *control*: the end of the stream, in either
direction, IS the signal. The end of the stream from the frontend means the
bridge is down (the shim says so, see THE UNUSABLE AND DOWN TEXTS). The frontend
treats a peer that is fully gone (not a peer's write-EOF alone) as the shim
having ended and frees the slot; it probes for a fully closed peer every 500 ms.
No frame type is sent in step 1.

## session

*session* is MCP JSON-RPC 2.0, newline-delimited, over the stream, served by the
same server as stdio MCP. It lists and serves exactly five tools: *whoami*,
*list-handles*, *restart-this-session*, *cancel-restart* and
*close-this-session*. A tool outside the five (*session-health* and
*update-this-session-description* among them) is JSON-RPC error -32602 with the
text *unknown tool: <name>* and is not listed. A call refused for want of a right
or because the caller is inside its unit is error -32001 with text beginning
*refused: *; every other failure is a tool result with *isError*. The request
never names the session acted on, nor the caller: both are the listener's. A
bridged call is CONFINED by construction: the classifier reads the origin the
listener put in the call's context, never the request bytes or the environment.

At most 8 calls are in flight on one stream; a ninth is refused (*refused: this
stream already has 8 calls in flight; wait for one to finish*). A reply is
written under a 10 second deadline. Two lifecycle verbs of one session never run
at once.

## Identity

For a call that arrived on the bridge, the session acted on is the listener's
session key and nothing else: not the environment's principal, not the
*principals/* mapping file, and not any argument. *whoami*, *list-handles*,
*restart-this-session*, *cancel-restart* (own form) and *close-this-session*
read the frontend-held record of that key (never the index; the record's own key
must be it) and act as the principal that record carries. When the key resolves
to no live record, or the record carries no principal yet, the call is refused
(-32001) and never falls back to the environment's principal:

- *refused: this bridge's session has no live frontend record, so there is no
  session to act on*
- *refused: this bridge's session record carries no principal yet (the
  SessionStart stamp is not there), so there is no session to act as*

## Refusals

When a service refuses a call for want of a right, the text has the form
*refused: <what> needs the right "<right>"; <who> can grant it (<how>)*. A text
names what is true: where a verb is gated only because the caller is inside its
unit, it has no right a session could hold (the rights *observe*, *close*,
*restart* and the rest are rights of a holder over a child, which a caller
inside its unit is not allowed to use), and it says who may do the thing instead
(the operator at a terminal outside the unit, or a holder with the right). A
policy refusal names the rule and who can lift it, never a path the session
itself has. No text carries a value from the request.

The older serve route is not an allow-list: it serves the whole registry and
every handler decides for itself (a deny gate), kept complete by a test that
gives every registered tool exactly one class. It goes away when clown stops
binding the serve socket into the unit (spinclass#525).

The always-ask floor lives only in the hook that runs inside the unit, so a
forced or widening act never rests on it over the bridge: the verbs refuse the
escalation arguments themselves, whatever their value, for any caller that
classifies as confined on ANY transport (the bridge, or the older serve route
that clown binds into the unit, where a call carries no origin). An unconfined
caller is unchanged and still meets the always-ask floor. The texts, exactly as
the frontend sends them (error -32001, the text prefixed *refused: *):

- *restart-this-session* with *force*, *fresh\_env* or any *posture*
  (*keep* included): *restart-this-session with force, fresh\_env or posture is
  not available to a session inside its unit; a holder with the right "restart"
  on this session, or the operator at a terminal outside the unit, runs
  `sc restart-child-session <session>` with --force or --posture (--force is
  always asked of the human); no command offers fresh\_env*. Nothing is recorded
  and no hand-off runs.
- *close-this-session* with *force* (any value): *close-this-session takes no
  force argument and never discards work; the operator at a terminal outside the
  unit discards work with `sc close --force <session>`*.
- *list-handles* with *accept* (or arguments that do not parse): *list-handles
  with accept has no right a session inside its unit can be granted; the
  operator runs `sc list-handles --accept` at a terminal outside the unit (a
  plain list-handles is allowed)*.
- *cancel-restart* naming a *child*: *cancel-restart naming another session
  needs the right "restart" on that session, which a session inside its unit may
  not use whatever it holds; a holder with the right, or the operator, runs
  `sc cancel-restart --child <session>` at a terminal outside the unit
  (cancel-restart with no child cancels this session's own)*.
- *cancel-restart* with no *child* cancels a restart this session queued itself.
  A restart the operator or a holder queued is not the session's to undo: the
  call is refused, the text names the kind of requester (never an id), and the
  request stays in place.
- *restart-this-session* without the trusted hand-off: *restart-this-session
  needs the trusted [session-entry].restart hand-off; the operator sets it in the
  global sweatfile or one above the repo (a repo or worktree sweatfile is
  ignored)*.
- *close-this-session* without the trusted setting: *close-this-session needs the
  trusted [permissions].allow-self-close = true; the operator sets it in the
  global sweatfile or one above the repo (a repo or worktree sweatfile is
  ignored)*.

The other policy refusals of *restart-this-session* and *close-this-session* (a
job still running, closing would orphan a session, unlanded work, a
main-checkout or closed session, a recorded principal the hand-off cannot name,
the posture rules) are refusals in the same form, each naming the rule and who
lifts it; they are -32001, and are logged and counted as *refused*. Their exact
wording is pinned by tests in *cmd/spinclass* (*bridge\_refusals.go*, *restart\_cmd.go*,
*close\_self\_cmd.go*). A verb with no named right that a session inside its unit
could hold (every other gated verb, should one reach a bridged stream) is refused
with the generic text *refused: <tool> is not available to a session inside its
unit; tell the session's holder or the operator, who run `<command>` at a
terminal outside the unit*. A session inside its unit has no channel to ask
anyone, so the text names who runs the command, not whom to ask.

An error is not a rule: a hand-off that will not exec, an unreadable sweatfile, a
failed write, or arguments that do not parse stay a tool result with *isError*,
logged and counted as *error*.

# ADMISSION: WHO MAY CONNECT

The listener admits a connection only when the peer's cgroup (read from its
pidfd, *SO\_PEERPIDFD*, never from a pid number) is EXACTLY the unit's
*ControlGroup*: strict equality, no descendant, no prefix, no suffix match
(empty, */* and a trailing slash are refused, and a peer with no pidfd is
refused, never admitted by a weaker check). The verdict is clown's
*pkgs/peercheck.AdmitPeer(conn, cgroupFile)*, which reads the file and then
makes the exact check (*CheckPeerExact*). The listener holds no checker of its
own: with none wired it refuses to start and says so. Our own read of the file
runs first, only to answer "not ready" while the file is missing and to refuse
a value that does not name the unit clown exported; the verdict is AdmitPeer's
own read of the file. *pkgs/peercheck.AdmitCgroup* stays in use for the
admission vectors.

The expected cgroup is read from the file named by *CLOWN\_BRIDGE\_CGROUP\_FILE*,
on every connection, never cached. The file MUST be a regular file (not a link,
not a FIFO) owned by the session's user, not writable by group or other (clown
writes mode 0600), outside the bridge directory, and its content is exactly the
cgroup path. A writer MUST NOT write a trailing newline; the reader tolerates
exactly ONE trailing newline and refuses two, or any other control character.
Until the file is valid the listener admits nobody, and the answer to a peer it
cannot yet admit (the file missing, empty, unreadable or not as above, or a peer
outside the cgroup) is *ERR refused <text>* and nothing else: the shim retries
*refused* and a different code would end its retry. The text names the reason in
plain words and carries nothing from the peer.

When clown drops the bridge after the listener started, it removes *bridge.sock*
and *bridge.cgroup* and logs it. The listener MUST notice either file gone after
it was once valid, close, and say so (it checks on a 2 second timer): a socket
that is not the file it created, or a cgroup file that was valid and is gone, is
a dead bridge, not a closed door to wait at. A cgroup file that was never written
is not death; it is *refused*, until it appears.

The *admission vectors* (*internal/0/bridge/testdata/admission-vectors.json*,
digest in *admission-vectors.json.sha256*, 21 cases) pin these behaviours for
both sides: each case is *{name, steps}* and a step is *{file, peer, admit}*; the
steps of one case share one cgroup file path, so a rewrite or removal between
steps shows the file is read per connection. *file* is *absent*, *dir*, *long*,
*symlink:<content>*, *abs:<content>* or *raw:<content>*; *peer* is the
connector's cgroup; *admit* is the expected decision.

# SHIM ACTION PER ERR CODE

The shim (clown's) acts per reply, by code only:

- *ERR version*, on *control* or on a data stream: the bridge is down for the
  whole shim.
- *ERR busy* and *ERR refused*, on *control*: retried in ONE loop for a 15
  second window, 100 ms doubling to 1 s, on an unreachable socket, *busy* or
  *refused* alike (no other retry exists); a window that ends without *control*
  marks the shim unusable. On a data stream they are said to that client at once,
  with no retry; the stream fails and the bridge stays up.
- *ERR unknown-service* and *ERR error*: said to that client; final, and the
  bridge stays up.
- Connect failed, EOF, no reply in time: on *control*, retried as above; on a
  data stream, the call fails at once with no retry. With *control* held only that
  connection fails; without it the shim marks the bridge down.

A failed data dial fails only that connection while *control* is held. The end of
*control* is the bridge being down. The retry loop is for *control* only.

# THE UNUSABLE AND DOWN TEXTS

When the shim cannot reach the frontend (no socket, refused connect, closed
stream, no reply within the timeout) it MUST fail the call at once with the DOWN
form and MUST NOT retry silently or fall back to a direct path. (The retries
above are the shim's connect attempts before the bridge is declared down, not a
fallback.)

```
clown: the bridge is down (<reason>); no tool or model call can leave this session; the session's frontend is gone or restarting
```

The DOWN form stays for the bridge being unreachable and for an ended *control*
stream. When the frontend answered, or the socket was reachable, but the bridge
cannot be used, the shim MUST say the UNUSABLE form instead
(*bridge.UnusableMessage*, vector op *unusable-message*):

```
clown: the bridge is unusable (<reason>); no tool or model call can leave this session; check the serve's log and spinclass session-health, and that the session's bridge setting matches the serve's
```

It is used for exactly two things: an *ERR version* on *control* or on a data
stream (the reason is the server's text, *version: server=N shim=M: <hint>*), and
a *control* stream not admitted within the retry window (the reason is the last
refusal text). The *<reason>* of the UNUSABLE form is printed on one line: every
control rune, every format rune (the bidi overrides among them), the unicode line
and paragraph separators and every byte that is not valid UTF-8 becomes one
space, and what remains is cut at a rune boundary to at most 200 bytes
(*MaxUnusableReasonBytes*) and ends in *...* when cut. The DOWN form applies no
bound of its own: its reasons are the shim's own fixed strings.

Where the text goes: a unix endpoint cannot carry it to its client (the shim
closes the connection, and the line goes to the shim's terminal log, rate limited
to one a second); only a tcp endpoint carries it, as the body of its 503 answer.

# LAUNCHER OBLIGATIONS (CLOWN)

These are what clown MUST do for the listener's checks to mean anything:

. Create the directory (*<runtime>/clown/launch/<key>/bridge/*) mode 0700, owned
  by the session's user, absolute, not a link, and start from it clean: remove any
  stale *bridge.sock*, *bridge.cgroup* and *bridge.cgroup.tmp* before the
  frontend starts. Refuse a link or a foreign-owned directory.
. Bind the directory into the unit READ-ONLY (*BindReadOnlyPaths=*). Nothing
  writable (no *BindPaths=*, no *ReadWritePaths=*) may cover the directory or any
  directory above it that the unit can reach, or the unit could replace the
  socket.
. Write the cgroup file OUTSIDE the bind
  (*<runtime>/clown/launch/<key>/bridge.cgroup*), mode 0600, content exactly the
  unit's *ControlGroup* path with no trailing newline, via *<file>.tmp* and a
  rename (a link in the way is replaced, not followed), as soon as systemd reports
  the unit active with its main process settled and BEFORE the provider starts.
  Refuse to write a value that does not end in */clown-agent.slice/<unit>*. The
  file is written once per unit.
. Set the three variables on the serve process only, and remove them from clown's
  own environment after routing them; empty when off (see THE ONE SWITCH).
. Wait up to 10 s after starting serve for *bridge.sock* (a socket, not a file).
  No socket means the frontend's listener did not start (a failed start: no exact
  peer check wired, a bad directory, a loose mode): clown binds nothing, sets no
  *CLOWN\_BRIDGE\_SOCKET* and advertises no *bridge-v1*; under *"offered"* the
  launch goes on, under *"required"* it is refused.
. Write the unit record after the provider has started (see START ORDER); the
  frontend reads it only for the session verbs.

# OBSERVABILITY

## Crossing log

The frontend MUST keep a log with one NDJSON line per crossing, at
*<state>/spinclass/bridge/<principal>.ndjson* (the directory mode 0700, the file
0600, created by the frontend; the session's unit has no access to it). A line
has the fields *ts* (RFC 3339 with nanoseconds, UTC), *session*, *service*,
*verb*, *outcome*, *duration\_ms*, *req\_bytes*, *resp\_bytes*, *arg\_names*,
*other\_arg\_names* and *args\_sha256*, and MAY have *fields* and *truncated*.
*outcome* is one of *ok*, *refused*, *error*, *busy*, *hello-timeout*, *version*;
a hello answered *ERR unknown-service* is logged as *refused* and one answered
*ERR version* as *version* (its own outcome: it means the shim and the frontend
were built apart). *service* is one of the reserved labels or *unknown*. A call
on the *session* service is one line whose *verb* is the tool name (*unknown* for
a tool the service does not list) and whose outcome follows the JSON-RPC result:
*ok*, *refused* when the error code is -32001, *error* otherwise. A hello that
did not become a stream is one line with verb *hello*; a served *control* stream
is one line with verb *stream*.

Redaction (normative): argument VALUES MUST NOT be logged, and neither may an
argument NAME a verb does not declare. A line carries *arg\_names*, the names
sorted (an empty list for none) of the arguments the verb's input schema
declares; *other\_arg\_names*, the number of other names the call carried; and
*args\_sha256*, the first 16 hex characters of the HMAC-SHA256 of the arguments'
canonical JSON (keys sorted, no insignificant whitespace; the raw bytes when the
arguments are not valid JSON) under a random key generated when the log opens and
never written anywhere. A line is at most 512 bytes including the newline: a
longer one loses its *fields*, then its *arg\_names* (they stay counted), then the
tail of its *session*, and carries *"truncated": true*. A per-verb allow-list may
add *fields*: a map of argument name to value for the names *session*, *branch*
and *target* only, each logged only if it is a string of at most 128 characters
matching *[A-Za-z0-9.\_/-]+*, otherwise dropped. Nothing from free text
(descriptions, messages, commands, reasons) is ever logged, and no attacker text
reaches *service* or *verb*.

The log directory MUST be a real directory (not a link) owned by the frontend's
user, tightened to 0700 when it is not; the file is opened without following a
link and without blocking, and MUST be a regular file of that user, else the
write fails (counted, not delivered to the crossing). The frontend's own log
lines about the listener (refused peers, refused hellos, panics) are rate
limited, one line per kind per 10 s, the next one carrying how many were
suppressed.

Retention: when a write would take the file past 4 MiB it is renamed to
*<principal>.ndjson.1* (older generations shift up; at most three are kept, *.1*
to *.3*), and log files older than 14 days are deleted when the log opens. A
failed log write MUST NOT fail or delay a crossing; it is counted and logged at
most once a minute.

## Metrics

Each stream is counted and timed as *spinclass.bridge.<service>.<outcome>* and
*spinclass.bridge.<service>.duration\_ms*. Each *tools/call* on the *session*
service is counted and timed as *spinclass.bridge.session.tool.<tool>.<outcome>*
and *spinclass.bridge.session.tool.<tool>.duration\_ms*; *<tool>* is one of the
five tool names with *-* as *\_*, or *unknown* for any other name (no text from
the request reaches a metric name); *<outcome>* is *ok*, *refused* or *error*, as
in the log. A peer refused before its hello is counted as
*spinclass.bridge.unknown.refused*. Emission is best effort and bounded: a burst
that finds every slot taken drops the metric.

## State file, wakes and notes

The serve writes *<state>/spinclass/bridge/<principal>.state.json* with its pid,
the start ticks of that process (field 22 of its */proc* stat, so a reused pid is
not taken for it) and a heartbeat. A live serve refreshes the heartbeat at most
once a minute: a 2 second poll only looks at the clock, and the file is rewritten
when a minute has passed. A reader (*sc list*, session-health) takes a
*listening* state as the last word of a serve that is gone when the pid is gone
or belongs to another process, or the heartbeat is older than three minutes, and
says *bridge DOWN: the serve that owned it is gone*. A state that says down
stands as written; a file with no pid is taken as it says. A serve that stopped
cleanly removes the file.

*sc list* and session-health also read the stored launch spec's *bridge* field and
clown's result (the unit record) *bridge* field, and say *bridge: asked for
(offered|required), not running: <reason>* for a live session whose spec asked
for the bridge when the result says *off: <reason>*, or says *recorded* and the
serve has written no state file. Absent (a clown that predates the field), a spec
that did not ask, and a session that is not running, say nothing. The reason is
clown's text, bounded to control-free words; it is data, not an instruction.

The frontend wakes the session's holders (and the spawner named in the record
when it has no holder) when the listener dies or fails to start, on the first
*ERR version* seen, and after five consecutive refusals of a peer (one is a stray
process, five is a bridge); a wake of one kind is sent at most once in 10
minutes.

# VECTORS

*internal/0/bridge/testdata/vectors.json* is a JSON array of cases with an *op*:
*hello* (stream bytes in *input*; expect the parse result, the reply line, and
*accept*; *rest* is what must stay unread), *reply* (stream bytes; expect *ok*,
*code*, *text*, and *rest*), *format-err* (code and text in; expect the reply
line), *socket-path* (*dir* or *dir\_gen* in; expect *path*/*path\_gen* and, when
given, *path\_len*, or *path\_err* of *too-long* or *empty*), *down-message*
(*reason* in; expect *message*), *unusable-message* (*reason* or *reason\_gen*
in; expect *message* or *message\_gen*) and *constants* (the values both sides
share: the socket name, the three variable names, the byte limits, the hello
timeout in ms, the major, the version hint, the reserved labels and the reply
codes; a side's own constants MUST equal them). *parse\_err* is *malformed* or
*too-long*. Any string may instead be given as a *\*\_gen* object
*{prefix, repeat, n, suffix}*. *served* defaults to all eight reserved labels.
Both implementations MUST pass every case. Invisible characters are written as
JSON escapes, never literally (a lint fails the build on a literal one).

*vectors.json.sha256* holds *sha256sum* output; a vendored copy MUST match it,
and a change to the file MUST change the digest and bump this text. The file has
92 array entries (a test run reports more than that, one per subtest and helper
case: count entries, not test lines). The admission file has 21 cases (see
ADMISSION). Spinclass runs both through its own reader and admission path
(*TestVectors*, *TestAdmissionVectors*), and clown vendors both byte for byte.
Drift between the two repositories fails a gate here: clown records the digests
it vendored as exported constants (*pkgs/bridgevectors.SHA256*,
*pkgs/peercheck.AdmissionVectorsSHA256*), and *TestVectorsDriftAgainstTheLockedClown*
in *internal/0/bridge* compares them with the two files, so publishing new
vectors without a re-vendor in clown, or locking a clown that vendored other
ones, is red and says to re-vendor.

# CONFORMANCE

- The vectors above: both implementations pass every case.
- *just debug-bridge-halves-proof <clown\_bin> <seam\_bin>*: runs spinclass's real
  *serve* listener against clown's real shim in a real transient unit, both start orders
  (serve before the unit, the unit before serve), with *control* admitted before
  any mapping or record exists, a *session* call refused until the mapping is
  written, *whoami* through the shim, a foreign caller refused, the cgroup file
  removed ending service, and an *ERR version* from a client inside the unit.
  *<clown\_bin>* is a pre-built clown and *<seam\_bin>* clown's separate
  *clown-bridge-seam* binary (which runs clown's real writers for
  *bridge.cgroup* and the unit record), both named by the caller; the recipe
  builds nothing outside this worktree.
- The tests named in the sections above, in *internal/0/bridge*,
  *internal/alfa/bridgeserver*, *internal/alfa/crossinglog* and *cmd/spinclass*.

# THE EGRESS EXTENSION (NOT STEP 1, NOT BUILT)

Provisional and unbuilt, recorded so that step 1 does not close the door: the
frontend stays the one door (accept, peer check, hello, label, policy, the open
logged) and hands the accepted connection to the service's owner (clown's outer
process) over a frontend-side unix socket by *SCM\_RIGHTS*. The peer credentials
of the passed connection stay the original connector's, so clown's proxy keeps
its own exact unit-cgroup check and no peer check is loosened (this is expected
behaviour, not yet tested here). Because the frontend no longer sees the bytes or
the close, the proxy reports a close frame *{id, bytes, outcome}* back on the
passing channel; the frontend keeps the stream slot and the open event and frees
the slot on the close frame, or on a lease timeout when the proxy dies without
one, so the 64-stream cap and the one observed event per connection still hold.
The hand-off socket needs its own exact peer check, a per-session path outside
the unit's binds, and a rule for an absent owner (*ERR error*, never a held
descriptor).

# KNOWN LIMITS AND UNVERIFIED

Every limit below is a tracked issue; the number is the place its removal
condition lives.

- Whether a relaunch of the session (an in-place restart) gives a new unit name,
  a new cgroup and a new frontend process: code-read by clown's owner (one unit
  name and one serve per clown process, so a restart is expected to be a new unit
  and a new serve). NOT observed. The first live run logs the serve's pid and
  *CLOWN\_BRIDGE\_UNIT* before and after one in-place restart. How long serve's
  stdio from clown lives: not checked. (#526)
- Between spinclass's read of the cgroup file and the admission check's own read
  of it there is a window only a process of the same account that sees the launch
  directory can use; the agent in the unit cannot. (#527)
- The shim's behaviour in this page (the retry loop, the ready lines, the 503
  body, *ShimExitRefused*) is clown's statement of its own code, checked by
  clown's owner against that code; spinclass's proof exercises the retry, the
  ready line and *ERR version*, not the unusable text of the shim, which only
  clown's unit test against a fake serve covers. (#529)
- A launch that never said hello has no principal, so the state-file note by
  principal is missing for it (the result-based note still shows). (#528)
- Same-account processes outside every unit can write the files the frontend
  trusts (the cgroup file, the unit record, the principal mapping, the state
  file); the checks stop other users and other units, not a process of the same
  account outside them.
- Host-side reads of files a worker can replace: the session record loaders and
  the hook sidecar open them O_NOFOLLOW and O_NONBLOCK, accept a regular file of
  at most 1 MiB, and treat anything else as an unreadable record. The tail
  reader of a spawn's *spawn.log* still does a plain read (a size cap would lose a
  real log's tail); its issue is not filed yet.
- The remaining low findings of the cut's review rounds are collected in #533.

## What this half serves, in plain words

The ruling names three kinds of stream: *control*, *session*, and stdio carriage
for host MCP servers. This listener serves two of them, *control* and *session*.
Stdio carriage is not in this half; a label for it, like every other label, is
answered *ERR unknown-service*. Nothing in an ordinary launch yet writes the
in-unit MCP client entry that opens the *session* stream (clown's
*bridge-stdio --service session* is the client; the first live run uses a
hand-made entry). (#530)

## The older serve route and a stop

*stop-this-session* is not one of the five session tools, so over the bridge it is
refused as an unknown tool. On the older serve route (clown's loopback relay,
calls with no origin) it exists; a confined caller there may not displace another
party's queued request or stop a session now (*when=now* is refused: the stop-now
right, a holder's *stop-child-session* or the operator's `sc stop <session> --now`
at a terminal outside the unit, is what permits it; the always-ask the tool
description names is the in-unit hook, which that route skips). It stops only
the caller's own session. A confined caller is also refused a restart of a
stopped session (a holder with the right "start", or the operator, runs the
command `sc start-stopped <session> --message <text>`). (#532)

## Deploy note: the floor tightens existing confined sessions

The refusals of *force*, *fresh\_env* and *posture* for a confined
*restart-this-session*, and of a confined session's cancel of a request another
party queued, apply to EVERY confined session on deploy, not only to sessions
launched with the bridge: they are keyed to the caller classifying as confined,
on both doors, outside the one switch. They only narrow what a confined session
could do before.

These verbs classify a call that carries no origin by EVIDENCE, never by
default. It is confined when the process runs inside an agent unit, when
spinclass wrote a confined launch for the session (the launch spec, written
before clown reports its unit record, so there is no window between the two),
when a unit record or the principal mapping says so, or when a record that
exists cannot be read or parsed (evidence exists, its content is unknown, so it
is refused, with the state named). With NO evidence at all (no record, no
launch, an unresolved principal or state directory, a serve with no record of
its own) the call behaves as before this change. The gated tools (*spawn*,
*merge*, *grit_\**, *perms*, and the rest of the gated set) keep their fail-closed
classification, and ALSO read the confined-launch evidence: a launch spec or
unit record spinclass wrote for the session's worktree, or one that cannot be
read, refuses them, so the window between a confined launch and clown's unit
record is closed for them as for the three verbs above. The
inputs are all host side: the serve's own cgroup and environment (set by the
frontend that started it, not by a call), and files under the frontend state
directory (the session records, the unit records and specs, the principal
mappings, the serve records), which a unit does not see. The terminal forms
of restart-this-session, stop-this-session and cancel-restart are confined only
when their own process runs inside an agent unit.

## The switch reaches only new spawns

*bridge* is written into a session's launch spec at spawn. Changing the knob
changes nothing for a session already launched: turning it on gives a running
session no bridge, and a session launched with *required* whose bridge cannot
come up keeps that setting through every restart, so clown refuses its launch.
There is no verb that flips it in place (a deploy-free flip is #531, with #519).
Recovery of such a session, with commands that exist today, run by the operator
at a terminal outside the unit:

- *sc list* shows the note "bridge: asked for (required), not running: <reason>".
- *sc close <session>* (a spawned child: *close-child-session*), which discards
  nothing unless *--force* is given; commit or land the work first.
- Set *[confinement] bridge = "offered"* (or *"off"*) in a sweatfile above the
  repo, then *sc start* or *spawn-session* again.

Use *"offered"* only in the first live runs.

The three written disagreements recorded with clown:

- A listener that does not start while clown waits 10 s for the socket: with one
  switch clown names the bridge only when it is on, so the wait ends early only
  on a failed start (no exact peer check wired, a bad directory); the earlier
  "gate off" case, and the log line that named the old stanza, are gone.
- The down-message wording is the vector's and this contract's: clown does not
  change it alone. A wording that names a local cause is a new vector first; the
  unusable form is that for a bridge that answered but cannot be used.
- A counter for refused connections on clown's side is not built by clown; the
  refusal is logged there, rate limited, and counted by the frontend (see
  Metrics).

# FILES

- *internal/0/bridge/*: the wire package, *testdata/vectors.json* and
  *testdata/admission-vectors.json* with their *.sha256* files.
- *internal/alfa/bridgeserver/*: the listener (control and session services).
- *internal/alfa/crossinglog/*: the crossing log and the state file.
- *cmd/spinclass/bridge\_\*.go*: serve's wiring, the session service, the
  refusals, the observers and wakes.
- *<runtime>/clown/launch/<key>/bridge/bridge.sock*: the socket;
  *<runtime>/clown/launch/<key>/bridge.cgroup*: the cgroup file.
- *<state>/spinclass/bridge/<principal>.ndjson*, *.state.json*: the crossing log
  and the state file.

# SEE ALSO

*spinclass-sweatfile*(5), *spinclass-internals*(7), *spinclass-build-pins*(7)
