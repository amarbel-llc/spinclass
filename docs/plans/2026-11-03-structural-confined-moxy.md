# Structural moxy access for confined workers (design note, 2026-11-03)

Design note, no code, no change to R3 or its test. A security-policy design:
the operator approves it before anything merges. Spinclass side only; clown's
side is named in section 5 and is clown's to confirm.

Labels: **R** read in code or docs this session, **V** observed (nothing new
was run for this note; V items are quoted from the cited docs), **I** inferred
or proposed, not checked. Sources: FDR 0035 "A confined spawn requires a
profile" (R1-R4); `internal/foxtrot/spawn/confineprofile.go`
(`checkConfinedToolSurface`) and `confine.go` (`buildUnitSpec`); clown FDR 0020
(in-unit servers, exclusions, the broker socket); clown
`docs/plans/2026-10-26-confined-moxy-sequencing.md` (frontend moxins); the #438
broker design; moxy `moxins/toolbox` (file list only).

Operator directive (via the coordinator): confined workers reach moxy, and only
the moxins the profile's moxyfile defines, with the resource rules the
sweatfile defines; this must be structural (more than that cannot be reached
from the unit); moxy and toolbox are in every profile by default.

## 1. What "structural" means

Premise (R, sequencing note, FDR 0035): the unit keeps the operator's uid and
is separated only by its mount namespace. Anything that runs in the unit can
read whatever is bound in and can run any binary it can reach, including its own
`moxy` with its own moxyfile. So a restriction enforced by a process in the unit
is policy, not a boundary (R, clown FDR 0020 "exclusions are policy").

Structural, here, is three properties:

- **S1, tool surface.** The set of moxy tools a unit can call is fixed by a
  moxyfile that the unit can neither read-modify nor replace, in a process the
  unit cannot signal, ptrace or restart. The unit reaches it only as a forwarded
  MCP stream.
- **S2, resource authority.** Which files and blobs a tool argument may name is
  decided by a process outside the unit (the broker, `spinclass serve`, from the
  trusted `[folio]` stanza), about the unit's view of the filesystem (section 3).
- **S3, no second route.** No moxy runs in the unit that a worker could point at
  a wider moxyfile or an unmediated filesystem. In practice: the in-unit moxy is
  gone from every confined profile (today it is gone from the worker's tool list
  by R3, but the binary and the in-unit moxyfile still exist, I).

What it does not claim: the frontend moxy still runs as the operator uid, so any
moxin kept in it is reachable with that authority. The boundary for a moxin is
whether it is safe to run that way (clown's "safe" condition: no filesystem or
exec effect for its caller beyond the broker's verdict, no caller-chosen target
the operator did not mean). That review gate stays an operator policy.

## 2. The shape: a per-session frontend moxy

(I, extending R: the frontend-moxin mechanism in `cmd/clown/frontendmoxin.go`.)

- Clown starts, per confined session, one `moxy serve-mcp` as a
  `scope:"frontend"` stdio server and forwards it into the unit through the
  bridge exactly like ringmaster, troupe and `spinclass serve` (R: the forward
  mechanism needs no spinclass spec change, sequencing note resolved question 3).
  Today clown does this per moxin (`serve-moxin --name N`); this generalizes it to
  one moxy over a moxyfile.
- The moxyfile is the profile's: a profile field clown owns, rendered by clown
  into frontend state, with `allow-tools` (dotted names allow one tool, R/V:
  moxyfile(5), sequencing note resolved question 2) and nothing else. It is not
  in any unit-writable path (clown already checks `pathWithin` against the spec's
  writable set for `MOXIN_PATH` and `moxy`; the same check applies to the
  moxyfile and to moxy's config discovery, section 6).
- The broker address given to this moxy is the real unix socket, not the
  `127.0.0.1:19001` relay (R: the relay exists so an in-unit moxy can reach it).
  The peer check (`pkgs/peercheck`, R) then sees a frontend process. This makes
  the broker's verdict binding: moxy is outside the unit and fails closed when
  the broker does not answer (R, ruling 6 of the #438 design).
- The unit's server list contains this moxy under a name that is not
  `moxy/moxy` (I: for example `clown-builtin-moxy/moxy`), so R3's literal test
  does not mistake it for the in-unit moxy.

## 3. How the frontend moxy sees the unit's files, not the operator's

The problem (rare-walnut's finding, R from the sequencing note: "the frontend
runs moxy and the moxin with the operator's authority"): a frontend moxy opening
`/home/<operator>/...` reads the real host, not what the unit has. The unit's
HOME is the agent-home under the worktree (R: `confinement.AgentHomeOf`,
clown `stageAgentHome`); `ProtectHome`, `deny_paths` and the read-only nested
binds differ from the host view (R: `buildUnitSpec`).

Two ways to make the views agree:

**V1, the broker as the floor, one host view (I).** The frontend moxy runs in
the host namespace. The broker (which already receives the spec's binds, R:
`unitSpec` is written by spinclass) adds an unconditional pre-check to every
resource verdict: the argument, after resolving `..` and every symlink to a
real path, must lie inside the unit-visible set (worktree and the bound `.git`
parts, minus `deny_paths`, with `home:` mapped to the agent-home) or the verdict
is `deny`. Weakness (I, and R for the underlying fact): the worktree is
unit-writable, so the unit can swap a path component for a symlink between the
broker's check and moxy's open (TOCTOU), and `folioeval` keeps a symlink leaf
as named (R, my moxy#500 review; the same hole exists for `folio.read` today).
V1 closes it only if moxy opens relative to a held directory fd with
`openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS)` (or checks `/proc/self/fd/N`
after opening and re-asks). Under that condition V1 is sound for reads; it is
not sound for any tool that writes or execs.

**V2, a twin mount namespace (I, preferred as the boundary).** Clown starts the
frontend moxy in a transient unit of its own that is given the same filesystem
properties as the agent unit (the same `binds_rw`, `binds_ro`, `deny_paths`,
`ProtectHome`, the same agent-home as `HOME`) and none of its network egress or
sockets. It is a separate unit in a separate PID namespace, so the worker cannot
signal or ptrace it, and the effective file view is the unit's by construction.
The consequence worth stating plainly: a file effect of the twin cannot exceed
what the unit's own native `Read`/`Bash` already can do on those paths, so the
broker's role in V2 is to narrow below the binds (the sweatfile can deny what
the bind allows), not to hold the line. Differences the twin tolerates: it does
not share the unit's private `/tmp` or `/run`; it sees the same bound inodes
(I: bind mounts of the same host paths), so a file written by one is read by the
other. Feasibility (I, unchecked): clown already builds the agent unit's
properties from the spec for `systemd-run --user`; reusing that builder for the
twin is the natural route, but I did not read clown's builder and a mount
namespace join (`setns`) is not an option for an unprivileged frontend.

Recommendation: V2 is the structural form; V1 with the `openat2` rule ships
first as the interim and stays in place under V2 as defense in depth. Honest gap
to say aloud: today (V1 without `openat2`) the frontend moxin path is not
structural for any file-taking tool; the issue-filer moxin is safe only because
it takes no path argument.

## 4. Reconciling with R3 and the grit concern

R3 exists because exclusions in an in-unit moxy are policy (R, FDR 0035 R3,
"form (ii) waits on the frontend proxy"). The frontend moxy is that proxy.
Proposed R3' (I, not built): a confined profile is accepted when

1. it keeps no in-unit `moxy/moxy` (today's rule, retained: S3);
2. it keeps `just-us/just-us` (unchanged);
3. if it keeps the frontend moxy server, the profile resolution reports that
   server's effective tool set (new clown profile-protocol field), and that set
   contains no `grit_*` tool. If clown does not report it, the spawn is refused
   (fail closed, same posture as R4's unknown source).

`checkConfinedToolSurface` and its tests stay as they are until clown reports the
field; the change then adds a branch and cases (a profile keeping the frontend
moxy with grit allowed is refused; without a reported tool set is refused; with
grit absent is accepted). The grit concern is the same one: `grit_*` through
moxy runs raw git with the operator's authority and bypasses spinclass's
ownership model (FDR 0036); the answer remains spinclass's `grit_*` store tools
in the frontend, never moxy's. The profile moxyfile must not allow `grit`, and
R3' makes that checkable instead of conventional. A tool added to an allowed
moxin later is the "denylist admits later tools" problem (R, FDR 0020
principle 1); an `allow-tools` list of dotted names (allowlist) avoids it, a
whole-moxin entry does not, so the default profile moxyfile should list tools,
not moxins, wherever a moxin could grow a tool with effects.

**Once grit leaves moxy (moxy#482, operator ruling 2026-11-03)** R3's grit rule
simplifies instead of growing: with no moxy `grit_*` tool to exclude, the
profile-reported tool-set check disappears and `checkConfinedToolSurface`
reduces to what remains true, "keep `just-us`" (so `run_recipe` works), plus
"keep no server that is not in the profile's reported allow-list" only if the
operator wants the ceiling checked at all. R3' would still need to check: (1) the
profile resolution reports a tool ceiling, not "every server" (the
`KeptServers == nil` refusal stays, since a non-selective profile has no
ceiling); (2) `catalogWidenings` compares the new ceiling field; (3) no kept
moxy tool is exec- or write-capable beyond the unit if Bash is denied. It would
no longer need a `grit_*` name list or the readback of the effective set for
grit's sake. Separately, spinclass#400 (done in the same branch, see FDR 0035's
bind table and probe F17) makes the worktree's `.claude/` and `.mcp.json`
read-only in the unit, which is the precondition for any `[claude] allow`
ceiling to mean anything under dontAsk.

Permission side (I, unverified): the hook's #372 moxy read allow list matches
`mcp__plugin_moxy_moxy__*` names (R: `moxy_read.go`). A frontend moxy under a
different plugin name will not match, so under dontAsk its tools need
`[claude] allow` entries (the same way the filer tool does, R). Whether to teach
the hook the new prefix (and let the broker be the only judge, as I argued for
the toolbox applets) is a decision for the operator.

## 5. What splits between spinclass and clown

| Piece | Owner |
|---|---|
| Profile field for the moxyfile; rendering it to frontend state; writability/symlink checks on it | clown |
| Starting the frontend moxy per session (frontend scope, forwarded); dropping the in-unit moxy from confined launches | clown |
| V2 twin unit (same filesystem properties, no network, own PID namespace) | clown |
| Reporting the frontend moxy's effective tool set in the profile protocol | clown |
| Handing the broker the real socket for the frontend moxy | clown |
| R3' in `checkConfinedToolSurface`; its tests; the FDR 0035 amendment | spinclass |
| The unit-visible floor in the broker (resolved real path inside the spec's set, `home:` = agent-home, `deny_paths` denied) | spinclass |
| `[claude] allow` for the new tool names; bind-probe additions | spinclass (operator's sweatfile content) |
| `openat2`/fd-relative open for resource arguments; no hierarchy/cwd moxyfile discovery in the frontend; per-tool resource declarations | moxy (grock/sunny-sumac, via the owning session) |
| What the default moxyfile contains; the moxin review gate | the operator |

## 6. Preconditions I could not verify

- **moxy's config discovery in `serve-mcp`.** The experiment that showed moxy
  ignoring a planted `./.moxy/moxins` and a cwd `moxyfile` was for
  `serve-moxin` with `MOXIN_PATH` set (R, sequencing note E3). It was not run for
  `serve-mcp`, which reads a moxyfile hierarchy. If the frontend moxy walks up
  from its cwd or reads `~/.config/moxy/moxyfile`, the unit-writable agent-home
  or worktree could narrow or, worse, supply configuration. The frontend moxy
  needs a cwd the unit cannot write, `HOME` outside the unit's write set, and
  ideally an explicit "no hierarchy" flag. Needs an experiment and a moxy issue.
- **Whether a moxy over `serve-mcp` can run in the twin** with the same
  properties as the agent unit: unread (clown's builder).
- **The hook allow list prefix** (section 4): read from my memory of the file in
  this session, not re-read for this note.
- **Context cost** (section 7): not measured.

### Re-checked 2026-11-03 (R)

- The hook prefix is confirmed: `mcp__plugin_moxy_moxy__` is hard-coded in
  `internal/delta/hooks/moxy_read.go` and twice in `hooks.go`. One consequence
  section 4 missed: the moxyfile-edit guard (`moxyfile_guard.go`) lists
  `folio_write`, `folio_cp`, `folio_mv`, `folio_link` and `folio_rm` by their
  full `mcp__plugin_moxy_moxy__` names. A frontend moxy under any other server
  name bypasses that guard silently. Under V2 the moxyfile is outside the twin's
  write set, so the guard is not the boundary there; in the V1 interim it is the
  only check, so the guard must learn the new prefix before a V1 frontend moxy
  gets any folio write tool.
- A review of this note found that the broker's peer check refuses a twin unit
  (it expects a frontend process, section 2). V2 therefore also needs the
  broker to accept the twin as a peer, or the twin's verdicts fail closed and
  every tool is denied. Clown's and spinclass's broker side (#438) both have a
  piece of that; not resolved in this note.

## 7. The default: moxy + toolbox in every profile

What the directive adds to the worker profile, and what I can say about cost:

- Toolbox tools (R, file list only): wc, uniq, tr, sort, sha256sum, b2sum,
  paste, digest, cut, plus the moxin's own page: about nine tools. Their schemas
  are small (typed options, one resource input each, per the FDR 0016 draft).
- Moxy's own meta tools (`status`, `restart`, `async`, `batch`; R: they are on the
  in-unit allow list) are not needed by a worker. I would leave them out of the
  default; `restart` is operator-only in any case.
- **Cost: not measured.** Honest estimate only: nine tools with short schemas
  is a small fraction of the current worker listing, but I will not state a
  number. A measuring recipe (`tools/list` byte size and token estimate for the
  proposed allow list, next to the existing `explore-prompt-cost` recipes) is
  cheap and should run before the default is fixed.
- Standing constraint to name: "never widen a default". This is a widening of the
  worker's default tool surface; the operator's directive is the authorization.
  Everything else beyond toolbox and its read-only companions (`env`, `man`) is a
  separate operator decision; `folio`, `rg`, `jq` duplicate native tools (R,
  sequencing note) and pay a context cost for little.

Proposed default frontend moxyfile for all profiles (I): `toolbox.*` plus
`env.*` and `man.*`; nothing that writes, execs, builds or reaches the network.

## 8. Questions for the operator

1. V2 (twin unit) as the target boundary, V1 with `openat2` as the interim: agreed?
2. Until V1's `openat2` rule is in moxy, should the frontend moxy be allowed any
   tool that takes a path, or only toolbox applets over blob URIs (chained
   results) in the interim? (My recommendation: toolbox on blob URIs only until
   the moxy change lands.)
3. The default moxyfile (section 7): toolbox plus env and man, or smaller?
4. Hook prefix: teach the hook the new plugin prefix, or require `[claude] allow`
   entries?
5. R3' fails closed when clown does not report the tool set: acceptable as the
   rollout gate?
