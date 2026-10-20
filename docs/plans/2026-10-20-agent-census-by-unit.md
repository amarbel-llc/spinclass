# Agent census by unit (FDR 0035 F11)

Status: implemented in `internal/confinement` (this branch). Context: clown
FDR 0021 §4.4 ("The agent census: identify agents by unit, not by name") and
FDR 0035's Limitations entry for F11.

## What F11 and the ancestry walk do today

- **F11** ("every live agent confined") has two halves:
  1. every live session in spinclass state has a parseable unit record
     (`LoadUnitRecord`); a session without one is named as unconfined;
  2. every process on the host whose `/proc/<pid>/comm` is in
     `DefaultClaudeComms` (`claude`, `.claude-wrapped`; renamed below) must sit in some live
     session's unit cgroup; offenders are named by the session whose worktree
     holds their cwd.
- **F2's ancestry walk** refuses a piggy-agent descending from a process whose
  comm is in the same list.

Defects: half 1 trusts the record alone, so a record whose unit has stopped
(a stale record) still counts as "confined" even though the session's agent,
if it has one, is not in it. Half 2 is spoofable (`prctl(PR_SET_NAME)`), misses
an npm-installed claude (`node`), and is blind to any harness not on the list,
such as clown FDR 0021's headless `juggler run` loop.

## What the census reads now

Authority runs from the frontend-written unit record to the kernel's cgroup
membership; names are the last and weakest input.

1. **Unit records (authoritative for sessions).** For every live session the
   record must load and validate as before (cgroup is a `.service` under
   `clown-agent.slice`), **and** its cgroup tree must hold at least one
   process now. A record whose cgroup directory is gone or empty is a
   *stale record*: the session is named ("unit X's cgroup has no processes:
   stale record"). Fail closed: a live session whose unit is not running has
   an agent we cannot account for.
2. **cgroup membership (authoritative for processes).** A process is confined
   iff its `/proc/<pid>/cgroup` is within some live session's unit cgroup.
   This is unchanged and is the only test that decides "confined".
3. **Which processes must pass test 2 (heuristic).** Agent runtimes outside
   every unit cannot be found structurally today: an agent clown never launched
   confined has no record at all, and nothing on the host lists "the agents
   clown launched unconfined". So the out-of-unit scan still matches names,
   now from two sources:
   - the names each live unit record reports it exec'd (`harness`, the
     optional field clown FDR 0021 proposes; read as a name or a path, reduced
     to its basename and truncated to the kernel's 15-byte comm), so any
     harness clown launches confined is automatically searched for outside
     the units too;
   - the fallback list `DefaultAgentComms`, now `claude`, `.claude-wrapped`
     and `juggler` (FDR 0021 §4.4's Stage 1 stopgap; clown builds juggler as a
     plain Go binary with `pname = "juggler"`, so no nix wrapper name is
     expected — UNVERIFIED against a running loop unit).
4. **F2's ancestry walk** additionally refuses a piggy-agent with any ancestor
   whose cgroup is under `clown-agent.slice` or a live unit (unit-based), and
   keeps the name check for an ancestor that is an unconfined runtime.

How a process outside any unit is detected: only by test 3. That is the
residual FDR 0035 already accepts — it catches honest unconfined sessions, not
a hostile process (which already holds the operator's uid).

## Failure modes

| Case | Result |
|---|---|
| live session, no record | F11 fails: "session K has no agent unit" (unchanged) |
| record, cgroup directory gone | F11 fails: stale record |
| record, cgroup present but empty | F11 fails: stale record |
| record, cgroup unreadable (EACCES, EIO) | F11 fails with the read error (not proof of staleness, but fail closed) |
| record with `error` set / bad shape | refused at load, counted as "no agent unit" (unchanged) |
| process exits mid-scan | skipped (ENOENT on comm or cgroup), unchanged |
| runtime renamed / not on any list | not detected (accepted residual; see Migration) |

The driver's own unit is checked the same way by F1's subject load; a stale
driver record already fails every unit-backed fact.

## Self-identification

Unchanged code, recorded here as the rule: a process identifies itself to
spinclass by the unit it runs in (cgroup, via `SO_PEERCRED` → pid →
`/proc/<pid>/cgroup`, as F2 and F12 already do), never by a name it asserts.
This is the same answer clown FDR 0021 gives circus#353 and moxy#486's
rejected client-name alternative.

## Migration

- `Inputs.ClaudeComms` / `DefaultClaudeComms` are renamed `AgentComms` /
  `DefaultAgentComms` and documented as the deprecated fallback.
- **Recommendation: no flag.** The name list stays on by default. A flag could
  only turn a fail-closed detector off, and nothing replaces it yet.
- **Removal condition:** the list can go when clown writes an agent record for
  *every* launch, confined or not (the pid it exec'd and its harness), into
  spinclass's state dir. F11's out-of-unit half then becomes "every recorded
  agent pid that is still alive is in a live unit", with no names at all. That
  is clown's change to make (its lane); spinclass already tolerates the
  additive `harness` field.

## Not done here

- No change to whether confinement is enabled, to sweatfile semantics, or to
  the unit record's version.
- Units under `clown-agent.slice` that no live session's record claims are not
  reported: their processes are confined, so they are not F11 offenders.
- Not run against a live confined spawn.
