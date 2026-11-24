# Private-view launch mode: spec shape for clown

2026-11-24, proposal. **R** = read in code/docs, **U** = not checked. For the
clown worker implementing the launcher half.

## What it is

An ordinary session (same uid, network, home, files, tools) in a private mount
view hiding named directories, plus a private pid namespace, so the operator's
agent and ssh control sockets are unreachable by path or via `/proc/<pid>/root`.
NOT a confined unit: no private network, home tmpfs or binds.

## Spec variant (clown FDR 0020 v1; `internal/foxtrot/spawn/confine.go` `unitSpec`)

Clown refuses unknown spec fields (R): one new optional field plus a capability.

- `"mode": "view"`. Absent = today's full unit, unchanged.
- In view mode clown applies ONLY `deny_paths` (inaccessible mounts, existing
  field), a private pid namespace with its own procfs, and the same result file.
  No private network, `ProtectHome=tmpfs`, binds or egress. A view spec with
  `binds_*`, `sockets`, `egress_allow` or empty `deny_paths` is refused.
- New capability `private-view` in `clown agent-scope-capabilities`; without it
  spinclass refuses the launch and names it (as `RequiredCapabilities`, mapping.go:292).
- `deny_paths` come from a trusted `[confinement] private-view-mask` list
  (default empty). Spinclass refuses a mask of `$HOME`, `$XDG_RUNTIME_DIR` or `/`.
  The real flic agent directory is **U**: reads of `~/.local/state/ssh` are denied
  to me; the operator must state the path.
- Result file, same shape: `unit`, `main_pid` (host namespace, R record.go:41),
  `principal`. Spinclass takes the host pid from `main_pid` when it is in the
  unit's cgroup (existing check, probes.go:81).

## The marker

Clown sets `CLOWN_PRIVATE_VIEW=<unit name>` in the view unit only, last in its
environment, scrubbed elsewhere, like `CLOWN_UNIT` (R, FDR 0035). It must be a
different variable: `CLOWN_UNIT` means "confined" to spinclass (R: grit verdicts,
hello pid, moxyfile guard, `session.InUnit`) and a view session is deliberately
not that. A hint, not a boundary.

## Spinclass half

1. Write the view spec, check the capability, set `CLOWN_AGENT_SCOPE`. Switch:
   trusted-layers-only `[confinement] private-view = true`, read live, off by
   default. A spawn parameter can follow later (it only narrows).
2. Hello pid: with `CLOWN_PRIVATE_VIEW` set the hello hook must not record
   `os.Getppid()` (a private-namespace pid), as under `CLOWN_UNIT` (R: hooks.go:667,
   and `recordRelaunchedPID` hooks.go:476). Today it WOULD, and `session.IsAlive`
   would test a wrong host pid. `RequestClose` already signals only a pid it can
   prove belongs to the session (R: FDR 0035 "Session PIDs are untrusted").
3. Liveness and close use `main_pid`.

## Checked / unchecked

- R: F10 (private pid namespace) passed on live units, so a user-manager unit has
  had one on flic. **U**: `PrivatePIDs=` on systemd 261.3 in a user-manager unit
  with `deny_paths` only; the clown worker proves it first.
- R: serve, merge gates, `host-load`, restart and stop run in the frontend and do
  not need the session's view of host processes. Recipes scanning `/proc` for
  host processes (`explore-live-*`, `debug-session-procs`) see only their own namespace.
- **U**: session scripts that signal a host pid. The mask covers the inode it
  started with, and a same-uid process still reaches whatever it does not name.

Done: a test session in this mode on flic cannot list the agent directory,
cannot see a host pid, and still merges and runs recipes.
