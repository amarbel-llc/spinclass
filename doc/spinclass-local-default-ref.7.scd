spinclass-local-default-ref(7) "spinclass" "Spinclass Reference"

# NAME

spinclass-local-default-ref - why a merge may leave the local default branch behind

# DESCRIPTION

A merge targets the default _remote's_ default branch (e.g. *origin/master*).
It rebases onto it, gates against it, and lands by pushing to it. Session
creation cuts new branches from the same ref.

The root checkout's _local_ default branch is never read for correctness.
After the landing push, spinclass fast-forwards it to the landed commit as a
convenience — using git's own *merge --ff-only* semantics — and reports the
result as an *advance local <branch>* step.

A local-only merge (*local\_only*) is the exception: its remote is the checkout
itself, so there the fast-forward of the local branch IS the landing, and a
refusal fails the merge.

# WHY A SKIP IS FINE

When the fast-forward is not possible, the step is reported as *# SKIP* and the
merge still succeeds. The operation that matters — the commit landing on the
remote — has already happened; failing it retroactively over a convenience ref
would misreport what occurred. Spinclass's own checks (close, clean,
auto-close) already count a branch as integrated when either the local default
branch or its remote-tracking ref contains it.

If you read the local default branch to decide whether work merged, read
*<remote>/<branch>* instead.

# RECONCILING

The skip reason names the cause and the command:

*uncommitted changes*
	Your edits in the checkout overlap the incoming change. Commit or stash
	them, then run *git merge --ff-only origin/<branch>*.

*diverged*
	The local branch has commits the remote lacks and vice versa. Rebase it
	onto, or reset it to, *origin/<branch>*; which one is your call.

*ahead*
	The local branch has commits the remote lacks. Push them, or drop them.

# METRICS

Each remote landing sends statsd counters to stats-me (*stats-me-clients*(7)):
*spinclass.merge.landed*, *spinclass.merge.push\_refused*,
*spinclass.merge.local\_advance.ok*, *spinclass.merge.local\_advance.skip* and
*spinclass.merge.local\_advance.skip\_reason.*{*dirty\_overlap*, *diverged*,
*ahead*, *error*}. *ok* plus *skip* always equals *landed*. Nothing is sent
unless *STATSD\_HOST* or *STATSD\_PORT* is set; *SPINCLASS\_DISABLE\_STATSD=1*
turns emission off.

# SEE ALSO

*spinclass-merge-this-session*(1), *spinclass-merge*(1), *spinclass-start*(1)

spinclass FDR 0029 (detached landing worktree), spinclass#295, spinclass#315.
