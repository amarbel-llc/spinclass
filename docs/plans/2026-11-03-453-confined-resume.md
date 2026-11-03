# #453: resuming a confined session that has no resume entry (positions, 2026-11-03)

Design note, no code. Labels: **R** read in code or docs, **V** observed,
**I** inferred or proposed. Sources: `cmd/spinclass/resume_confined.go`,
`cmd/spinclass/commands_session.go` (the resume path), `internal/echo/shop`
(`PinEntry`), `internal/alfa/session` (`PinnedEntry`), FDR 0035, FDR 0037,
spinclass-sweatfile(5) `[session-entry]`, clown's
`docs/plans/2026-10-26-unattended-clown-confinement.md` (clown#318) and
clown#336. Concrete case: `spinclass/keen-hemlock`, spawned confined, refused
by `sc resume` with "has no [session-entry].resume" (V, the issue).

## 1. Why the refusal happens (R)

- `[session-entry]` start, resume and env are resolved once, at creation, from
  the trusted layers (global, ancestors, main checkout) and pinned on the
  session state (`PinnedEntry`); `sc resume` replays the pin and reads no
  sweatfile (`entryFor`). A confined spawn pins whatever those layers say, and
  today no layer sets `resume`, so the pin has none.
- With no resume entry `sc resume` falls back to the start entry, a shell, in
  the worktree. For a session launched confined that would open an unconfined
  shell in a once-confined worktree, so `confinedResumeScope` refuses, with no
  opt-out (spinclass#400, the sticky-confined rule). The refusal is right; the
  gap is that nothing provides the entry it asks for.
- When a resume entry exists, the same function hands clown the unit spec as
  `CLOWN_AGENT_SCOPE` (applied last, so nothing configured can name another),
  checks the entry is a harness resume (no `-c`, `--continue`,
  `--fork-session`, `--from-pr`), and the resume path splices the recorded
  profile (`--profile <name>` before the entry's `--`) and `{principal}` (the
  session's recorded clown principal, UUID-shaped, refused otherwise). clown's
  own sidecar re-confines any launch that names a confined session and refuses
  a changed or missing spec (clown#318, R from clown's note). So the relaunch
  path exists end to end; only the entry is missing.

## 2. Positions

### (1) A default resume entry for the clown harness

The documented clown form is `["clown", "--", "--resume", "{principal}"]`
(spinclass-sweatfile(5)): clown is the frontend, `--resume <principal>` after
the separator is the provider's resume of the same transcript, and clown's
sidecar re-applies the recorded unit spec. Two places could supply it:

- **At resume time, for a confined session only (I, recommended).** In
  `confinedResumeScope`'s shell-fallback branch, when the session was launched
  confined and the pin has no resume, use that default instead of refusing.
  Covers sessions that already exist (keen-hemlock). Unconfined sessions keep
  the shell fallback: no default widens for them, and the default never runs
  unconfined because the unit spec is handed over on the same path. A trusted
  `[session-entry].resume`, when set, still wins (it is pinned).
- **At pin time, for a confined spawn (I).** `PinEntry` for a confined spawn
  records the default when no layer sets `resume`, so the state shows what
  will run. Does not help a session pinned before the change.

The default is a spinclass constant for the clown harness, not a sweatfile
value: the sweatfile already lets an operator choose another harness, and a
repo layer may not choose it (the pin reads trusted layers only, R). The
`{principal}` and profile splices already apply. Two open points, both small:
whether a `--no-attach` resume of a confined session should splice
`--permission-mode dontAsk` as the spawn did (clown's note says the dontAsk
splice is spinclass's, R); and that a session whose login is broken inside the
unit relaunches into the same broken login until clown#336 lands, which is a
reason to ship (2) with (1).

### (2) The refusal text

Today it says "resume it once it has a harness resume entry, or close it". It
should say what to configure, verbatim, and what close costs:

- the line for the global sweatfile (`~/.config/spinclass/sweatfile`):
  `[session-entry]` / `resume = ["clown", "--", "--resume", "{principal}"]`,
  and that with (1) the default makes this unnecessary for clown;
- that `sc close <key>` removes the worktree and the branch, and how much it
  would discard: commits not on the default branch and uncommitted tracked
  changes, counted at refusal time (cheap git calls on a path the frontend
  reads anyway); `sc resurrect` recreates the worktree from the close-time
  commit (FDR 0027) but not uncommitted edits.

With (1) in place the refusal remains only for a non-clown harness or a
non-resume entry, so this is mostly for those.

### (3) `restart-child-session` and a session stuck at login

- A restart is "spinclass authorizes and records, clown relaunches when the
  turn ends; an idle child is relaunched at once" (FDR 0037, R). A session
  stuck at the harness login has no turn, so from clown's status file it is
  idle or `unknown` (no file yet), never `busy-too-long`: `session-health` does
  not name the state, and the idle restart would fire and relaunch into the
  same login prompt.
- Position (I): the restart tools should keep covering it (no new refusal:
  the holder's `restart` right is the authority, and a relaunch is what the
  operator would do), but a restart does not fix it. The fix is credential
  delivery (clown#336). What spinclass can add on its own is honesty: a
  `session-health` verdict for "frontend alive, agent never started a turn
  for longer than the bound", which needs clown to record the login/startup
  state in its status file (a clown ask), else it stays `unknown`.

## 3. Options for the operator

1. **(1) at resume time for confined sessions, plus (2) and the (3) position
   as written** (recommended): keen-hemlock becomes resumable with no config;
   one spinclass change, test-first; clown#336 stays the fix for login.
2. **(1) at pin time only**: new confined spawns carry the entry; keen-hemlock
   still needs the global sweatfile line from (2).
3. **No default; (2) only**: the operator adds the global sweatfile line once;
   spinclass only improves the refusal. Smallest, but every host needs the
   line and a missing one is found at the worst time.
4. **Defer resume; extend `restart-hung-child` to a login-stuck verdict**:
   needs clown's status field first, and still relaunches into a broken login.

## 4. Ruling and what was built (2026-11-03, later the same day)

Operator ruling: option 1. Built test-first in `cmd/spinclass/resume_confined.go`:
`defaultConfinedResume` (`["clown", "--", "--resume", "{principal}"]`) is
returned by `confinedResumeScope` for a confined session whose pin has no
resume; the resume path then treats it as a harness resume (profile and
principal splices apply; a session with no recorded principal is refused). A
never-confined session keeps the shell fallback. The refusals (missing spec,
non-resume flags, unknowable state dir) now count what `sc close` discards
(`closeCost`: commits not on the default branch, uncommitted tracked changes,
`sc resurrect` recovering only the close-time commit) and the non-resume-flag
one prints the global sweatfile line. The `--no-attach` question: decided
conservatively, no permission mode is spliced on a resume; the pinned entry
runs as it is, and an unattended confined relaunch is a `sc spawn` matter.
The restart position is recorded in FDR 0037's hung-children section. No
`session-health` verdict for login was added: it needs clown's status file to
record startup state first.

## 5. Not covered here

The operator's installed binary being behind the session's (noted in the
issue) is deployment, not this design. Credential delivery into the unit is
clown#336 and is not solved by any option above.
