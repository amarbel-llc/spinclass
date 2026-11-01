# spinclass#438: resource policy through a broker in `spinclass serve`

Date: 2026-11-01. Status: design, ruled by the operator in the #438 grill (the
Google Doc "spinclass#438: policy broker and the bridge translator hop" holds
the diagrams and the per-round corrections). Nothing is built. Supersedes the
hook-first slices of `2026-10-27-folio-stanza-design.md`; that draft's stanza
grammar (section 2) and specificity question (section 6.1) carry over
unchanged. Labels: **R** read in code, **I** inferred, **U** unverified.

## 1. Rulings

1. **Moxy owns the resource model and the permission engine.** Tool
   arguments that name files or blobs become *resources* with an operation,
   so files and `madder://blobs/<digest>` go through one door, and any moxin
   can take a resource as an argument. (Grill decision B; the earlier idea of
   copying moxy into spinclass and maturing it there is withdrawn.)
2. **Policy is delivered over a moxy policy-broker protocol**: a standing
   connection, modelled on clown's plugin protocol for handshake and
   initiation, not a process per call.
3. **For a spinclass session the broker is `spinclass serve`** (decision A,
   held at 80%): it already holds the session identity, the handles and the
   trusted sweatfile layers.
4. **Option (i): serve listens on a socket of its own; moxy dials it.** Not
   a second socket on the clown-stdio-bridge: the bridge's translator is a
   single-session pipe keyed on bare JSON-RPC ids (R), so multiplexing it is
   the larger clown change.
5. **Clown names the broker path and exports it.** No handshake change, no
   deterministic-path convention. Prior art (go-plugin, `SSH_AUTH_SOCK`,
   systemd socket activation, D-Bus): the launcher names the socket, the
   service listens, the client dials.
6. **Verdicts on the wire: `allow`, `ask`, `deny`.** The sweatfile's
   `unmatched` is the broker's last rule, so the broker always answers.
   Moxy's compiled-in static floor (today's CWD and nix-store rule) applies
   in exactly one situation: no answer (no broker configured, unreachable,
   hello refused, deadline missed). `ask` is today's exit-1 prompt and is
   `deny` under dontAsk. *(Ruled 2026-11-01: the three-verdict form, and
   permissions always fail closed: no absence, error or timeout ever widens
   what the floor allows.)*
7. **The check carries the subject explicitly**; the connection only
   authorizes "this peer may ask for that subject". The hello carries the
   session key both ways. *(Ruled 2026-11-01. The operator expects the
   subject to fall out of the markl provenance chain, FDR 0032 and troupe#39,
   once that exists; until then it is the session key.)*
8. **Clown migrates to dagnabit first; `pkgs/` is defined by export
   directives.** Landed 2026-11-01 (clown e1bcb59, 882c06c, clown#328): the
   peer check is `code.linenisgreat.com/clown/pkgs/peercheck`; spinclass's
   lock is at 882c06c (aa90f56).
9. **Folding moxy into clown is deferred.** When it comes: copy moxy into
   clown; decouple and refactor the engine; export the engine from clown;
   make the external moxy consume it, still delivering moxy standalone
   (krone's `mcp-origin` runs `moxy serve-http` without clown, R).
10. **The PreToolUse hook shrinks but stays**: the dontAsk ask-to-deny
    conversion, the background-shell floor, the sc-verb and store tools, the
    main-worktree guard. It may later be a second broker client.

## 2. The processes

| Process | Launched by | Role |
|---|---|---|
| `clown` (plugin host) | the session entry | launches every plugin server, reads each handshake, health-checks it, then starts the agent (R: RFC-0002 §3.2 to §3.8). Allocates the broker path and exports it to the agent's environment; confined: adds a relay row and sets it in the in-unit moxy's `Env`. |
| `clown-stdio-bridge` | clown | wraps serve; unchanged. |
| `spinclass serve` | the bridge | MCP over stdio as today, plus a broker listener on the path clown named, peer-checked with `pkgs/peercheck`. |
| `moxy` | Claude Code (unconfined) or `clown agent-relay` inside the unit (confined, R) | dials the broker, asks per resource, applies the verdict or the floor. |

Serve's stdio loop is concurrent, one goroutine per message (R, purse-first
`libs/go-mcp/server/server.go`), and no lock a merge holds covers a policy
read (R: the merge lock and the liveness flock are not held across reads; the
session-record flock covers one read-modify-write), so a check cannot queue
behind a long tool call.

## 3. The protocol (sketch, modelled on RFC-0002 and RFC-0005)

- **Startup.** Serve listens on `$CLOWN_POLICY_BROKER` (name to be fixed with
  clown), socket 0600 in a 0700 directory clown owns. Moxy dials it; serve
  authenticates the peer with `peercheck` (SO_PEERCRED pid, then the unit's
  cgroup when confined; the relay is in the unit, so it passes; unconfined:
  same uid, which is also what protects the socket file). Hello both ways:
  protocol version, capabilities, the session key, so moxy refuses a wrong
  broker.
- **Per call.** `check {subject, op, resource}` with a deadline (tens of
  milliseconds, to be measured). Serve evaluates the trusted `[folio]` layers
  and the live handles and answers one verdict.
- **Crash.** Serve dying alone drops the connection; moxy falls to the floor
  and reconnects with backoff. A session restart relaunches everything (R:
  FDR 0037), so that case needs nothing special.
- **Resources.** `{kind, uri}` plus an operation: `file:///...` with
  read/write/exec, `madder://blobs/<digest>` with read. The grammar for
  patterns is the 2026-10-27 draft's.
- **As specified (moxy RFC 0002, 39f9341, 2026-11-01).** NDJSON `{v, type}`
  with hello/check/verdict/error; the client hello lists the subjects it
  serves (empty adopts the broker's `session_key`); check carries an id,
  subject, op, resource and `deadline_ms` (default 100 ms, unmeasured).
  Variable `CLOWN_POLICY_BROKER`, agreed with clown#333: a value starting
  with `/` is a unix socket path (unconfined); otherwise `host:port`, the
  in-unit relay's loopback address (confined). Clown exports no session-key
  variable; `SPINCLASS_SESSION_ID`, which spinclass exports, is the optional
  "wrong broker" check where present.
- **The moxy PreToolUse hook is a second client (found by the moxy worker).**
  Under Claude Code the hook decides the prompt in its own short-lived
  process before the call reaches the moxy server, so a verdict inside the
  server cannot remove a prompt. The hook dials, hellos, checks and closes
  per call over the same wire; the server re-checks over its standing
  connection; both fail closed on no answer. The standing connection is the
  broker's; the per-call hook client is a Claude Code constraint.

## 4. What each repository does, in order

1. **clown** (next, clown#333): allocate the broker socket path per session,
   export it to the agent, relay it into confined units and set it in the
   in-unit moxy's `Env`.
2. **moxy** (next, moxy#491; moxy#490 and moxy#485 adjacent): the resource
   model; `rg_search` behind the same door; the broker client with the
   hello, the deadline, the static floor on no-answer, and `ask`/`deny`
   mapped onto the existing `perms-request` exit codes.
3. **spinclass**: the `[folio]` stanza (grammar and trust from the
   2026-10-27 draft; `unmatched` required), the broker listener in serve on
   `pkgs/peercheck`, the evaluator over trusted layers and accepted handles
   (`spinclass:children` stays an authority anchor), the hook shrink. The
   first slice that removes the prompts in #438 (reads of sibling worktrees)
   is then a global-sweatfile stanza, no hook change.
4. Later: profile-level patterns and spawner narrowing (the child's set a
   subset of the parent's; the small grammar keeps it comparable).

## 5. Open

- Allow-over-deny specificity (2026-10-27 draft, 6.1): longest literal prefix
  wins, deny wins ties. Decide before the evaluator is written.
- Confined views: the broker runs in the frontend and evaluates the path the
  caller named; worktree and state binds are same-path, the staged agent-home
  is not. Until a mapping exists the broker treats an agent-home path as
  unmatched.
- Per-check latency and the deadline value (U): a unix-socket round-trip
  benchmark as a debug recipe, then re-measure with the real evaluation.
