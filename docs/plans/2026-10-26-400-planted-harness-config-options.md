# #400: a confined worker can plant harness config an unconfined resume loads

2026-10-26, spinclass/firm-mulberry. Options for the operator to choose from;
no code. Evidence labels: V verified (code read), R reported, I inferred.

## The problem

A confined worker (FDR 0035) has its session worktree mounted read-write. The
harness launched by a later unconfined `sc resume` runs in the operator's
terminal, outside the unit, and reads project configuration from that
worktree. Whatever the worker wrote there runs or applies with the operator's
authority.

What is in reach:

- `<worktree>/.mcp.json` and `<worktree>/.claude/settings.local.json`. spinclass
  writes both at setup, and `WriteMCPConfig` merges into an existing
  `.mcp.json` rather than replacing it, so a planted entry survives
  `sc rebuild` (R, the issue; the merge behaviour is V in
  internal/claude/mcp.go per the issue's citation, not re-read today).
- Other project files the harness reads that spinclass does not write: I,
  from general Claude Code behaviour, not checked against the installed
  version. Examples: `.claude/settings.json` (permissions and hooks; a hook
  is a command, so it is code execution), `CLAUDE.md` (instructions),
  `.claude/commands`, `.claude/agents` and skills. Any fix that covers only
  the two files spinclass writes leaves these open.
- The worktree's `.envrc`, if the resume path loads it through direnv: I.
  direnv ties an allow to the file's content, so an edit should revoke it,
  but this was not checked.

## Options

1. **Regenerate and replace on resume.** Rewrite both spinclass-written files
   from trusted sources (sweatfile hierarchy, frontend state) before the
   harness starts, replacing rather than merging. Small. Covers only the two
   files, so it does not close the class. It also discards an operator's own
   edits to them.

2. **Move them out of the worktree.** Keep the MCP config and settings in
   frontend state and point the harness at them (claude's `--mcp-config`,
   `--strict-mcp-config`, `--settings`: I, flag names from memory, to be
   checked). The worker cannot write them. Still covers only those two files,
   plus whatever project-scope reading `--strict-mcp-config` turns off. It
   needs the launch argv, which clown builds, so it is a clown/spinclass
   change together.

3. **Refuse (or ask) on drift.** Record a digest of each harness-read file in
   the frontend record when spinclass writes it or when the confined session
   ends; on an unconfined resume, compare and refuse, naming the files, or ask
   the operator (always-ask) with the diff. Covers any file put on the list;
   unlisted files stay open. False positives after the operator's own edits.

4. **A session launched confined resumes confined (sticky).** An unconfined
   resume of a once-confined session is refused, or is an always-ask operator
   escalation that names the risk, like `--unconfined` on spawn. Closes the
   whole class at the boundary FDR 0035 already draws: whatever the worker
   wrote only ever runs inside a unit. Costs operator flexibility, and needs a
   confined resume to exist for the session to be resumable at all (I: not
   checked whether `sc resume` can relaunch confined today).

5. **Ignore project-scope config on an unconfined resume of a once-confined
   session.** Launch the harness with project and local setting sources off
   (claude `--setting-sources`: I, unverified that the installed version has
   it) and `--strict-mcp-config`. Covers settings and MCP; `CLAUDE.md` and
   skills may still load. Depends on harness flags (clown's lane).

## Recommendation (for the operator to decide)

Option 4 as the default, with option 3 as the operator's escape: an
unconfined resume of a session launched confined is an always-ask that
lists the harness-read files that changed since the unit started. It is the
only option that does not depend on enumerating every file the harness reads
or on harness flags, and it reuses FDR 0035's existing escalation shape.
Options 1, 2 and 5 are hardening worth having later, not a fix for the class.

## Open questions for the operator

- Is resuming a once-confined session unconfined a workflow you rely on?
  If not, option 4 can simply refuse.
- Should the same rule cover `sc exec` (an operator shell in the worktree runs
  direnv and any project tooling), or only the harness?
