## Worktree management

This Claude Code session is running inside a spinclass-managed worktree at
`.worktrees/<name>`. Worktree creation, branch hygiene, and cleanup are
owned by spinclass. Do NOT call `EnterWorktree` or `ExitWorktree`, do NOT
delete the worktree directory yourself, and do NOT ask the user whether to
"exit" or "clean up" the worktree on your own initiative.

After `mcp__spinclass__merge-this-session` succeeds, stay on the existing
spinclass branch in the same worktree to start the *next* piece of work.
Do NOT create a new branch — `merge-this-session` leaves the worktree's
branch in place precisely so it can keep accumulating commits across many
merge cycles. Do NOT create a new worktree per piece of work either —
spinclass worktrees are long-lived workers, not subject-scoped branches.

A merge does NOT lock your worktree. The slow `pre-merge` build/test gate
runs in a dedicated, detached build worktree — not this one — and the merge
fast-forwards exactly the commit it pinned when it started. So you may keep
editing and committing in the session worktree while a merge runs; any edits
you make meanwhile are simply left for the next merge (never lost, never
half-merged). The only step that briefly touches the session worktree is the
synchronous prefix (rebase, plus the optional `[hooks].repair` phase) — and
with `merge-this-session-async` that completes BEFORE the job id is returned,
so the moment you hold the job id the worktree is yours again to work in.

If the user explicitly asks to leave or destroy the worktree, defer to
them.

## Session description

When it becomes clear that the session is resolving to a specific task —
a GitHub issue, a ticket, a concrete fix, or any well-defined goal —
call `mcp__spinclass__update-this-session-description` with a short
imperative description of that task (e.g. "fix login redirect loop",
"add JIRA-1234 webhook handler"). Do this once, as soon as the task
crystallises, and do not update it again unless the task fundamentally
changes.

## Session-local scratch space

The session's `.tmp/` directory (pointed to by `$TMPDIR` and
`$CLAUDE_CODE_TMPDIR`) lives inside the worktree. When the session is
closed (`sc close`, which removes the worktree), `.tmp/` goes with it,
so you do NOT need to clean up files you create under `.tmp/` — leaving
them there is fine.
