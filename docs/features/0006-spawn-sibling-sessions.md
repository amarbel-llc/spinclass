---
status: proposed
date: 2026-05-02
revised: 2026-06-11
implemented: 2026-06-11
promotion-criteria: |
  Promote to `accepted` when the motivating driver/worker pattern (see
  Problem Statement) has been exercised end-to-end through the new tool
  IN PRODUCTION: spawn, chat hello, brief delivered, worker merges,
  completion wake — with no manual terminal-juggling. The implementation
  (docs/plans/2026-06-11-spawn-sessions.md) landed 2026-06-11 with bats
  e2e covering spawn/hello/brief/timeout over stub templates; the
  worker-merges + completion-wake legs await the first real spawn.
---

# Spawn sibling-repo sessions

## Problem Statement

A cross-repo driver/worker coordination pattern has emerged organically
in real use: a "driver" Claude Code session running in repo A needs work
done in repo B, hands the work to a "worker" session there, and
continues with unblocked work until the worker signals completion.

Two production observations bracket the design:

- **2026-05-02 (original)**: the dodder→madder migration ran a 7-hour
  driver session (`dodder/deft-sequoia`) coordinating two manually
  started workers (`madder/rare-buckeye`, `madder/smart-banyan`) through
  GitHub issues + `Monitor` watches. Everything was automated except the
  spawn: the user opened terminals and ran `sc start` by hand.
- **2026-06-10/11 (post-chat)**: the crap#22 handoff from
  `spinclass/bright-cedar` to `crap/mild-catalpa` coordinated entirely
  over the FDR 0010 chat system — a rich driver-written brief, a
  follow-up addendum, and a "released, your move" completion wake. The
  GitHub issue existed as the durable work item, but the *protocol* was
  chat. The spawn step was again the only manual action.

This feature gives the driver session a tool to spawn worker sessions
in a sibling repo. Everything else — the chat protocol, merge cycles,
session lifecycle — already exists.

## Locked-in design (revised 2026-06-11)

The 2026-05-02 draft predated the chat system (FDR 0010) and implicit
sessions (FDR 0014). Every original locked-in was relitigated against
that new constraint space on 2026-06-11; the results below supersede
the originals.

### Coordination protocol — chat-only

Driver↔worker coordination is the FDR 0010 chat system, full stop: the
spawn delivers the driver's brief, the worker's first chat message is
the health handshake (see Kick-off), and progress/completion arrive as
chat wakes via clown's job-watch monitor. GitHub issues are **optional**
— a driver may still file one when the work item deserves durable
tracking, but issues are not part of the spawn contract and the tool
never requires or creates one.

(Supersedes: "GitHub issues + Monitor exclusively." The 06-10 crap#22
handoff demonstrated chat carrying the entire protocol in practice.)

### Addressing — repo dirname

The spawn target is the sibling repo's **directory name** (`crap`,
`madder`), the same identity already load-bearing in session keys and
chat targets. Resolution searches the user's workspace roots for a
repo directory with that leaf name; an explicit path remains accepted
as an escape hatch.

Root discovery is **deferred**: across all current hosts and workspace
roots (`~/eng/repos/*`, `~/eng-*/repos/*`) the leaf names are unique
and do not collide, so v1 may resolve by leaf search without a
configured root list. A sweatfile `[spawn].roots` glob list is the
designated shape if collisions or search cost ever materialize.

(Supersedes: path-based v1 with alias follow-up.)

### Substrate — sweatfile spawn template

Spinclass does not hard-code a multiplexer. The spawn execs a
cascade-merged sweatfile argv template, mirroring the `[[remotes]]`
attach template pattern, with zmx as the shipped default:

```toml
[session]
spawn = ["zmx", "attach", "{id}", "--detach", "{entry}"]
```

`{id}` is the worker's session id; `{entry}` expands to the worker's
entry argv (see Autonomy). tmux users override one line; the driver's
TTY is never touched.

(Refines: hard-coded `zmx run`. Corrected 2026-06-11 after the first
production spawn: the draft default used `zmx run`, which types the argv
as space-joined keystrokes into a shell — a multi-line brief would be
shell-interpreted — and does not understand `--`; `attach` with a command
execs the argv directly and `--detach` returns promptly. FDR 0001 had
already flagged `zmx run` as unsafe. See #145.)

### Autonomy — interactive, with harness kickoff

The worker is a normal, attachable, resumable interactive session — the
`claude -p` one-shot mode remains rejected. But an idle login shell is
not a worker: the spawned session must come up with a **harness**
(clown for this workspace; claude for others) already running the
brief. The mechanism is a sweatfile argv template:

```toml
[session]
spawn-entry = ["clown", "--", "{prompt}"]
```

(Example corrected 2026-06-11: clown's grammar is `clown [clown-flags]
-- [provider-args]` — a bare positional is rejected as an unknown flag,
killing the worker at boot; the brief must ride after `--`.)

Harnesses accept an initial prompt as a positional argument, so this is
"typing clown into the prompt and hitting return" without keystroke
injection: the spawned zmx session execs the harness directly with the
driver's brief as its first input. After the harness exits, the session
behaves like any other (attach, resume, close).

`spawn-entry` is distinct from `[session].start` deliberately:
human-started sessions keep their interactive-shell entrypoint;
spawned workers boot straight into the harness.

### Kick-off — synchronous until chat hello

The driver tool blocks until the worker's **hello chat message**
arrives, with a 60s default deadline (see Tuning Levers); failure or
timeout inside the window surfaces as a tool error to the driver. This
gate proves the full stack — multiplexer session, entrypoint, harness,
hooks, chat plumbing — where the previously-leaned PID-alive probe
proved only that a process existed.

The hello is emitted **mechanically by the worker's `SessionStart`
plugin hook** (the FDR 0014 infrastructure): the spawn records the
driver's session key in the worker's state (`spawned_by`, below), and
the hook, seeing it, chat-sends the hello to that key. Deterministic —
it fires the moment the harness is up, with no reliance on the model
reading and obeying an instruction in the brief.

(Supersedes: sync-until-healthy with PID-alive lean. The healthy-gate
open question is resolved by construction.)

### Worker initial context — driver-written brief

The spawn tool's required input is a **brief**: a driver-composed
prompt blob (the crap#22 handoff brief is the reference shape — richer
than any issue body, carrying diagnosis, pointers, and the
message-me-back instruction). An issue number is an optional add-on
that prepends the issue body to the brief. The brief becomes the
worker harness's initial prompt via `{prompt}`.

(Supersedes: required GH issue number / `start-gh_issue` shape. Also
resolves the initial-context-extensions open question: multi-issue or
doc-pointer context is just content in the brief.)

### Lineage — spawned_by in worker state

The worker's session state records the driver's session key in a
`spawned_by` field, surfaced as an annotation in `sc list` and
`chat-list-sessions`. One cheap field answers "why does this session
exist", doubles as the driver's spawn log, and leaves tree-style
display as a later cosmetic.

(Resolves the worker-visibility open question.)

### Failure after handoff — presence probe on suspicion

A worker that dies silently emits no chat wake (dead producers cannot
emit — the same known limitation as async merge jobs). v1 adds no
heartbeat machinery: `spawned_by` plus the PID-liveness that
`chat-list-sessions` / `sc list` already compute gives the driver (or
the user) a sharp "is my worker alive?" probe whenever something feels
overdue. Tying into FDR 0012 session presence is the designated
upgrade path if instinct-driven probing proves insufficient.

(Resolves the failure-modes open question.)

### The same machinery powers `sc fork`

The launch half of spawn — the `[session].spawn` multiplexer template,
the `[session].spawn-entry` harness boot, the hello gate, and
`spawned_by` lineage — is deliberately not cross-repo-specific.
`sc fork` (branch a new session off the current worktree) gains the
same detached-launch path: a fork can come up as a harness-booted,
chat-addressable worker in its **own** repo, briefed by the forking
session, instead of requiring the user to attach a terminal to it.
Spawn = "worker in a sibling repo"; detached fork = "worker on a
branch of this repo"; one launch mechanism underneath.

## Sketch — interface

```
sc spawn <repo-dirname> --brief "<text>" [--issue <N>] [--description "<text>"]
```

(Named `sc spawn`, not `spawn-sibling`: dirname addressing made the
target a repo name rather than a filesystem sibling, and the launch
machinery also backs detached forks.)

- `<repo-dirname>`: leaf name of the sibling repo (explicit path
  accepted as escape hatch). Must resolve to a different repo than the
  driver's.
- `--brief`: required; the driver-written kickoff prompt.
- `--issue`: optional; prepends the named issue's body to the brief.
- `--description`: worker session description; defaults to a
  derivation from the brief's first line.

MCP tool: `spawn-session`, same inputs. `sc fork` grows the matching
detached mode (flag shape decided at implementation-plan time) reusing
steps 3–5 below verbatim.

Effects:

1. Resolve the dirname to a repo root (error: not found, ambiguous,
   same repo, or nested worktree).
2. Create the worker session as `sc start` would (worktree, sweatfile,
   trust), with `spawned_by` set to the driver's session key.
3. Exec the `[session].spawn` template, which launches the
   `[session].spawn-entry` harness with the brief.
4. Block until the worker's hello chat message (60s default deadline;
   error on timeout).
5. Return `{session_key, worktree_path, multiplexer_session}`.

## Tuning Levers

- **Hello deadline (60s)**: comfortably past typical harness startup
  (10–30s), far inside MCP request timeouts, no async twin needed.
  Signal to raise it (or grow a `[spawn].hello-timeout` knob): cold
  nix-cache session starts — direnv/devshell evaluation on first
  attach — exceeding the window in practice.

## Deferred

- **`[spawn].roots` configuration** — only needed when dirname leaves
  collide across workspace roots or leaf search cost matters.
- **Tree-style lineage display** (`sc list --tree`) — cosmetic atop
  `spawned_by`.
- **Heartbeat / FDR 0012 presence integration** — upgrade path for
  silent-death detection.
- **Cross-machine spawning** — same-host only in v1; zmx's SSH
  workflow plus `[[remotes]]` make this a plausible v2.

## Limitations

- **Same-host only.** No remote spawning in v1.
- **Silent worker death is invisible** until someone probes presence;
  the hello gate covers only the startup window.
- **Harness must accept a positional initial prompt.** A harness
  without that CLI shape needs a wrapper script in `spawn-entry`.
- **No spawn cleanup on driver close.** Workers are real sessions; the
  user manages them via `sc list` / `sc close`.

## More Information

- Origin observations: dodder→madder migration sessions
  (`f94e6853…`, `3b5c4231…`, `2c1feec9…`, 2026-05-02) and the crap#22
  chat handoff (`spinclass/bright-cedar` ↔ `crap/mild-catalpa`,
  2026-06-10/11).
- FDR 0010 — chat / clown job-wakeup producer (the coordination
  substrate).
- FDR 0014 — implicit sessions (the SessionStart hook infrastructure a
  worker hello can ride).
- FDR 0012 (draft) — session presence; the designated failure-detection
  upgrade path.
- `[[remotes]]` attach templates (FDR 0011) — the sweatfile argv-template
  pattern `[session].spawn` mirrors.
- Issue #59 — tracking issue with the 2026-06-11 decision log.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown);
revised 2026-06-11 with all locked-ins relitigated post-FDR-0010/0014.
