# Bridge wire, contract v1: design history

**The normative contract is the manpage `spinclass-bridge(7)`
(`doc/spinclass-bridge.7.scd`).** This file kept that text while the bridge was
being built; principle 21 (reference material is a manpage shipped with the
derivation, not a plan document) moved it there. What stays here is the history
and the reasoning behind the choices, which a manpage should not carry. Clown's
FDR 0048 is the design record on the other side; the vectors in
`internal/0/bridge/testdata/` are the shared conformance files.

## What the bridge is for

A confined session runs in a unit that cannot reach the frontend's files or
sockets. The bridge is its one narrow door to spinclass's own services: one
listener per session inside that session's `spinclass serve`, one unix socket,
in a directory clown binds read-only into the unit. Step 1 is the `session`
service (five verbs over MCP) and the `control` service (a stream whose end
means the bridge is down). Egress is an extension, recorded in the manpage and
not built.

## Decisions and why

**The caller is the connection, never the request.** The identity of a bridged
call is the listener's session key and the peer's cgroup, read from its pidfd.
A request that names a session, a principal or an environment is ignored: the
first round of review found verbs that read the environment's principal, and the
fix was to make the listener's origin the only source.

**Exact peer check, no suffix match.** The first version admitted a peer whose
cgroup merely ended in the unit's name. A cgroup under the unit, or a different
unit with a matching tail, would have passed. The check is now strict equality
against the file clown writes, read on every connection, and the listener has no
checker of its own: with none wired it refuses to start. Clown's
`peercheck.AdmitPeer` (it reads the file, then `CheckPeerExact`) is that check
since the lock moved to clown 5da0c521.

**A directory bind, read-only.** The socket lives in a directory clown binds
read-only into the unit, and nothing writable may cover it or any directory
above it, or the unit could replace the socket. The cgroup file lives outside
the bind for the same reason.

**`control` is admitted on identity alone (J-H1).** The first start order gated
the listener on the principal mapping and the unit record. Both are written only
after the unit is up, and clown waits for the socket before it starts the unit,
so the bridge could never come up. Then a second version gated `control` on the
same records, which moved the deadlock inside the shim's retry window: the unit
record is written after the provider starts, the provider starts only after the
shim holds `control`. The ruling: serve binds at once; `control` is admitted on
the exact cgroup check plus the unit suffix taken from the environment; readiness
(the mapping, the record) gates only the `session` verbs, per call, where it costs
nothing because nothing calls before the provider exists. Clown's owner found the
second deadlock in his own code; the two-real-halves proof
(`just debug-bridge-halves-proof`) shows both start orders.

**One switch (J-M5).** There were two: a sweatfile stanza the frontend read and
clown's own environment variable. Two switches meant two things to flip and a
state where one was on and the other off. The one switch is the launch spec's
`bridge` field, set from the trusted-only knob `[confinement] bridge`, with
`"offered"` (launch continues without a bridge) and `"required"` (launch
refused). The listener starts only when clown exports the three variables
non-empty, and clown does that only when the field asked.

**The force floor holds on every route (J-M1).** The always-ask floor lives only
in the hook inside the unit. Over the bridge a verb refuses `force`, `fresh_env`
and `posture` itself. The same session can also reach the frontend by the older
route clown binds into the unit, where a call carries no origin; the verbs now
ask whether the caller classifies as confined on any transport.

**Liveness (S-M2).** A state file that says "listening" was believed forever,
even after its serve was killed. It now carries the serve's pid, the start ticks
of the process and a heartbeat, and a reader treats a dead writer as down.

**Shared vectors, one home.** The wire has two implementations (spinclass's and
clown's shim). The vectors and the admission vectors live in spinclass's
`internal/0/bridge/testdata`, with `.sha256` files, and clown vendors them byte
for byte. The first admission case on which the sides differed (one trailing
newline in the cgroup file) was decided by the shared file: the reader tolerates
exactly one, the writer writes none. Invisible characters in the vectors are JSON
escapes, because the build lints for literal ones.

## Review and proof history

Round 1 (two reviewers) found the suffix check, the unbounded accept loop, the
force floor, identity from the environment and the open socket directory. Fix
batches F1 to F5 closed them, in that order, with tests; the joint round with
clown's owner then found J-H1 (the start-order deadlock), the older route
(J-M1), the cancel-restart overwrite (S-M1), the liveness gap (S-M2), the
"asked for, not running" gap (J-M2) and the shim/contract differences (J-M3).
The two-real-halves proof ran both start orders against clown's real shim, built
by clown's owner, with clown's own writers for the cgroup file and the unit
record.

## Not observed

Whether an in-place restart gives a new unit name and serve, and how long
serve's stdio from clown lives, are code-read by clown's owner and never
watched. The first live run logs them. The shim's statements about itself (retry
loop, ready lines, the 503 body, `ShimExitRefused`) are clown's, checked by
clown's owner against his code, not by spinclass.
