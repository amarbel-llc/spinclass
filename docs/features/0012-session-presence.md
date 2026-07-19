---
status: proposed
date: 2026-06-07
promotion-criteria: working implementation behind an unset-by-default sweatfile key; presence columns render correctly against a real zmx config for 1 week without probe-failure diagnostics
---

# Session presence: attached clients and agent activity

> **Draft.** Design approved, not yet implemented. Sections describe intended
> behavior.

> **Post-cutover note (2026-06-14):** cross-session chat left spinclass
> entirely and is now a clown construct — see
> [FDR-0017](0017-clown-session-attach-grouping-chat-rescope.md). The
> `chat-send` / `chat-read` / `chat-list-sessions` tools and the `chatroom/`
> store referenced below have been removed; those references describe the
> pre-cutover state.

## Problem Statement

`sc list` and the resume picker show a session as `active` when the single
attach PID is alive, and their "time ago" freezes at attach/exit time. That
model can't answer the questions that actually matter when scanning sessions:
is anyone attached right now (locally or via ssh/mosh reattach that bypassed
`sc resume`), and is the agent inside the session busy or idle? Attached
clients and agent activity are orthogonal axes — a session can be agent-busy
with zero humans watching, or human-attached with no claude running — and
spinclass currently reflects neither.

## Interface

Two read-time data sources, derived at render time and never persisted into
`state.json`:

### Presence probe (attached clients)

A new sweatfile key, scalar-override merge like the other `[session]` keys:

```toml
[session]
presence-probe = ["sh", "-c", "zmx -g spinclass list --json | jq '[.[] | {worktree: .cwd, clients}]'"]
```

- Exec'd **once per render** (`sc list`, resume picker, resume warning), with
  a timeout. Stdout contract: JSON array of
  `{"worktree": "<abs path>", "clients": N}` objects. Rows join to sessions by
  worktree path.
- Unset key = feature dormant: no probe runs, client data renders as unknown.
  spinclass is not coupled to zmx — any multiplexer (or script) that can
  report worktree→client-count implements the seam.
- `sc validate` checks the key is a well-formed argv.

### Agent beacon (claude running / busy)

`spinclass serve` — the MCP server claude launches inside the worktree, so its
lifetime equals claude's — writes `<worktree>/.spinclass/agent.json`
(`{pid, started_at}`) at startup and touches it (mtime bump) on every MCP tool
dispatch, via a wrapper applied at tool-registration time.

Read side, per session:

- agent **running** iff `agent.json` exists and its recorded pid is alive;
- agent **busy** iff running and the file mtime is within the busy threshold;
- otherwise no agent. No delete-on-exit is needed — pid liveness handles
  crashes and SIGKILL alike.

### Derived data and wire format

A derived struct (`internal/session/presence.go`):

```go
type Presence struct {
    Clients       *int       // nil = unknown (no probe / probe failed / no joined row)
    Agent         string     // "" | "running" | "busy"
    AgentActivity *time.Time // beacon mtime, nil if no live agent
}
```

`ListRow` gains optional `clients`, `agent`, `agent_activity` fields
(omitempty). Remote hosts serve `spinclass list --format json`, so remote rows
carry presence for free — each host derives live against its own probe and
beacons. Old remote binaries omit the fields, which renders as unknown; no
version negotiation.

### Display

- `sc list` text/table: new `CLIENTS` (`0`/`1`/…, `-` unknown) and `AGENT`
  (`busy`/`running`/`-`) columns.
- Resume picker Detail:
  `active · 2 clients · claude busy · 3m ago · @branch · repo`, omitting
  unknown/none segments. `LastActivity()` prefers the agent beacon mtime when
  fresher than `ExitedAt`/`StartedAt`, which unfreezes the relative times.
- `chat-list-sessions` rows gain the same presence text (agent presence is the
  relevant signal when picking a DM target).

### Behavior: client-aware resume warning

`resumeConfirmPlan` gains presence input. If `Clients != nil && *Clients >= 1`,
the resume confirmation shows the warning variant
"N client(s) attached — attach anyway?" with the same semantics as today's
`active` warning: default Cancel, `-y` does not skip, non-TTY errors. If
presence is unknown (probe unset/failed), the existing PID heuristic applies
unchanged — deleting the sweatfile key reverts behavior entirely, which is the
rollback procedure.

## Examples

List with presence (zmx-backed probe configured):

    $ sc list
    KEY                      STATE              CLIENTS  AGENT    ...
    spinclass/sleek-locust   active             1        busy
    piggy/light-birch        running-detached   0        running
    tap/clear-cherry         running-detached   0        -

Resume picker rows:

    fix login bug      active · 1 client · claude busy · 2m ago · @sleek-locust · spinclass
    bump deps          running-detached · 0 clients · 3h ago · @light-birch · piggy

Resume collision warning:

    $ sc resume spinclass/sleek-locust
    ⚠ 1 client attached — attach anyway?  [Cancel]

## Limitations

- **Probe-shaped truth.** Client counts are only as accurate as the configured
  probe; sessions attached through a path the probe can't see report unknown,
  not zero.
- **Beacon coverage.** Agent presence requires claude to load the spinclass
  MCP server; a bare claude without it reads as no agent. Mid-turn lulls
  between tool calls longer than the busy threshold read as `running` (idle)
  even though a generation is in flight.
- **One beacon per worktree.** Two claude instances in one worktree
  last-writer-win on `agent.json`; the file records a single pid. Documented,
  not defended against.
- **Resume-warning fallback.** With the probe unset the warning keys off the
  attach PID exactly as before — the new accuracy only exists where the seam
  is configured.
- Probe failure, timeout, or unparseable stdout degrade to unknown with a
  TAP/`--verbose` diagnostic, never a hard error.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| busy threshold | 120s | tool calls cluster tighter than this mid-turn | sessions read idle during long single-tool generations, or busy long after a turn ended |
| probe timeout | 2s | matches the liveness probe | probe times out under load or over a network fs |
| beacon bump granularity | every tool dispatch | cheapest correct signal | mtime churn shows up in profiling |

## More Information

- FDR 0001 — worktree-local session state (state machine, liveness probe, the
  `running-detached` state this feature refines the display of).
- FDR 0011 — remote sessions (the `list --format json` wire that carries
  presence fields across hosts).
- [#119](https://code.linenisgreat.com/spinclass/issues/119) — inventory of
  spinclass-generated worktree files (`agent.json` adds to it).
