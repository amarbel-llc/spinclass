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

If the user explicitly asks to leave or destroy the worktree, defer to
them.

## Session-local scratch space

The session's `.tmp/` directory (pointed to by `$TMPDIR` and
`$CLAUDE_CODE_TMPDIR`) lives inside the worktree. When the session is
closed (`sc close`, which removes the worktree), `.tmp/` goes with it,
so you do NOT need to clean up files you create under `.tmp/` — leaving
them there is fine.
