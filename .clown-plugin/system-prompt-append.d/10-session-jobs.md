## Background jobs (merge/check)

When deciding between the synchronous `merge-this-session` /
`check-this-session` and their `-async` twins, **consult your task
list**: if it holds pending or in-progress items beyond the merge
itself, go async — make progress on them while the pre-merge hook
runs, and the `[clown-job]` wake will interrupt you with the result.
If the board is empty, either call the synchronous tool (simplest) or
go async and end your turn — the wake re-invokes you when the job
finishes.

Never start an async job and poll `session-job-status` in a loop. If
you went async and ran out of other work with the job near
completion, `session-job-wait` blocks for the result; for a job with
real time remaining, end your turn and let the wake arrive.
