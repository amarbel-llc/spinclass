# Bridge wire, contract v1 (text for FDR 0048 "Wire")

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119. The
reference implementation is `internal/0/bridge` (standard library only); its
`testdata/vectors.json` is normative where this text is silent.

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
at 64+512 bytes. After an `ERR` the server closes the connection.

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

## Control stream

`control` is the one stream a shim holds open for the life of the session. The
frontend uses it to learn the shim is alive (its loss means the session is
gone) and to push notices; its framing is the control service's, not the
wire's. At most one `control` stream per session is open; a second is `ERR busy`.

## Limits and timeouts

At most 64 open streams per session; the 65th hello is answered `ERR busy`. The
server MUST receive the whole hello line within 5 seconds of accept, else it
closes the connection without a reply. Hello and reply bounds are above.

## Refusal text

When a service refuses a call for want of a right, the text has the form
`refused: <what> needs the right "<right>"; <who> can grant it (<how>)`. Over
MCP the refusal is JSON-RPC error code -32001. When no policy exists at all the
text is `nothing crosses; the operator enables the bridge with <switch>`.

## Failure text

When the shim cannot reach the frontend (no socket, refused connect, closed
stream, no reply within the timeout) it MUST fail the call at once with
`clown: the bridge is down (<reason>); no tool or model call can leave this session; the session's frontend is gone or restarting`
and MUST NOT retry silently or fall back to a direct path.

## Crossing log

The frontend MUST keep a log with one NDJSON line per crossing, at
`<state>/spinclass/bridge/<principal>.ndjson` (the directory mode 0700, the file
0600, created by the frontend; the session's unit has no access to it). A line
has the fields `ts` (RFC 3339 with nanoseconds, UTC), `session`, `service`,
`verb`, `outcome`, `duration_ms`, `req_bytes`, `resp_bytes`, `arg_names` and
`args_sha256`, and MAY have `fields`. `outcome` is one of `ok`, `refused`,
`error`, `busy`, `hello-timeout`; a hello answered `ERR unknown-service` is
logged as `refused` and one answered `ERR version` as `error`. `service` is one
of the reserved labels or `unknown`. A call on the `session` service is one
line whose `verb` is the tool name (`unknown` for a tool the service does not
list) and whose outcome follows the JSON-RPC result: `ok`, `refused` when the
error code is -32001, `error` otherwise. A hello that did not become a stream
is one line with verb `hello`; a served `control` stream is one line with verb
`stream`.

Redaction (normative): argument VALUES MUST NOT be logged. A line carries
`arg_names`, the argument names sorted (an empty list for none), and
`args_sha256`, the first 16 hex characters of the sha256 of the arguments'
canonical JSON (keys sorted, no insignificant whitespace; the raw bytes when the
arguments are not valid JSON). A per-verb allow-list (`SafeFields`) may add
`fields`: a map of argument name to value for the names `session`, `branch` and
`target` only, each logged only if it is a string of at most 128 characters
matching `[A-Za-z0-9._/-]+`, otherwise dropped. Nothing from free text
(descriptions, messages, commands, reasons) is ever logged, and no attacker
text reaches `service` or `verb`.

Retention: when a write would take the file past 4 MiB it is renamed to
`<principal>.ndjson.1` (older generations shift up; at most three are kept, `.1`
to `.3`), and log files older than 14 days are deleted when the log opens. A
failed log write MUST NOT fail or delay a crossing; it is counted and logged at
most once a minute.

## Vectors file and its digest

`internal/0/bridge/testdata/vectors.json` is a JSON array of cases with an `op`:
`hello` (stream bytes in `input`; expect the parse result, the reply line, and
`accept`; `rest` is what must stay unread), `reply` (stream bytes; expect
`ok`, `code`, `text`, and `rest`) and `format-err` (code and text in; expect the
reply line). `parse_err` is `malformed` or `too-long`. Any string may instead be
given as a `*_gen` object `{prefix, repeat, n, suffix}`. `served` defaults to
all eight reserved labels. Both implementations MUST pass every case.
`vectors.json.sha256` holds `sha256sum` output; a vendored copy MUST match it,
and a change to the file MUST change the digest and bump this text.
