# A second merge runner, outside `spinclass serve`

Status: design only, nothing built. The first half of parking (stop a
session's agent while its merge waits, start it when the merge ends); the
parking half is designed after the operator reviews stop and start
(docs/plans/2026-11-20-stop-start-rights.md).

## Problem

An async merge or check job runs as a goroutine in `spinclass serve`
(internal/alfa/job/runner.go), and serve is a plugin child of the session's
frontend. When the agent stops, serve exits and the job dies with it:
ringmaster marks it gone through the per-job flock, the merge lock is
released, and nothing lands. The stacked-merge queue (FDR 0025) lives in the
same process and is lost too. So a session cannot be stopped while its merge
waits for the merge lock or a host gate slot, which is where parking wants to
stop it.

## Constraint

The operator's rule for the rollout: the in-serve runner stays, and the new
runner is added beside it behind a per-session switch, so sessions can be
moved one at a time (canaries) with no regression for the others.

## Interface

### The switch

A session record field, `MergeRunner`: empty is today's in-serve runner,
`process` is the new one. It is set per session by a holder with the `park`
right through a new tool, `set-merge-runner child runner`, and by the operator
from a terminal; each change is journalled. A trusted-layer default
(`[hooks].merge-runner`, layers above the repo only) can come later, for the
fleet-wide switch once canaries are clean; the session field overrides it.

A session launched confined keeps the in-serve runner for now: the process
runner would run outside its unit (open question 3).

### The process runner

`merge-this-session`, `merge-this-session-async`, `check-this-session` and
its async twin keep their interface. With `MergeRunner = process`:

1. `PrepareMerge` runs in serve, synchronously, as today (it needs the session
   worktree and returns the pinned sha before the job id).
2. Serve starts the runner as a transient systemd user service
   (`systemd-run --user --unit=spinclass-job-<id> --collect`), not a scope, so
   it survives serve's exit, running a hidden `sc job-run --job <id>` with the
   prepared merge passed as a file in the session's state directory.
3. The runner writes the same job record (internal/alfa/job) and the same
   spool, holds the ringmaster per-job flock itself, runs `FinishMerge`, and
   writes the terminal record and the completion wake to the session's
   channel, exactly as the in-serve runner does. `ServePID` becomes the
   runner's PID.
4. The synchronous tools wait on the job through ringmaster (`job wait`), so
   their 4-minute detach (FDR 0010) is unchanged.

Cancel moves to one path for both runners: `session-job-cancel` records a
ringmaster cancel request, which the runner's existing cancel observer
(`ringmaster wait --on-cancel`) acts on. The in-serve runner already has the
observer, so its behaviour is unchanged.

Stacked merges (FDR 0025): with the process runner a second merge while one
runs is refused rather than queued, until the queue is persisted (open
question 2). Parking will disable async merges for a parking session anyway.

## Failure modes

- Runner crash: the OS releases the job flock (ringmaster reports gone), the
  merge flock and the host-queue slot, as for a serve crash today; an orphaned
  landing worktree is reaped by `sc clean`, as today.
- Serve exit while the runner runs: the point of the runner; the job lands
  and wakes the channel. Without parking nothing restarts the agent, so the
  wake is read at the session's next start.
- systemd-run unavailable or failing: the merge is refused before anything is
  pinned, naming the cause, with no fallback to the in-serve runner (a silent
  fallback would hide a canary's regression).
- Two runners for one session: refused at start by the existing per-worktree
  running-job check, now read from the job record rather than serve's memory.

## Canary plan

1. One idle-heavy session moves to `process`; its merges are compared with
   the in-serve ones for result text, timing and wake delivery.
2. Metrics `merge.<repo>.runner.process.*` beside the existing ones.
3. Widen session by session; the default flips only on the operator's word.

## Open questions

1. Who besides `park` holders may flip the switch: a coordinator-level
   holder only, or the session itself for its own merges?
2. Persist the FDR 0025 queue (a file per session) now, or refuse stacking
   under the process runner until parking needs otherwise?
3. Confined sessions: start the runner inside the session's agent unit (it
   then dies with the unit) or as its own confined unit?
4. Should check jobs move too, or only merges (a check never takes the merge
   lock, so it rarely waits long)?

## Size (a guess)

4 to 6 days of spinclass work with tests, about half of it in the job
runner's split and the cancel path.
