# Disable merge via sweatfile

## Motivation

In environments where the default branch is protected (e.g., work repos that
require PR review), the existing `sc merge` / `merge-this-session` flow is
unsafe: an agent can drive a fast-forward merge into `master`/`main` from
within a session, bypassing review. This change adds a sweatfile-level kill
switch that disables both surfaces. The pre-merge hook — which is meant as
agent CI — must remain reachable independently, so this design also
introduces `sc check` and `check-this-session`.

## Config schema

New optional field on the existing `Hooks` struct:

```toml
[hooks]
disable-merge = true
```

- Type: `*bool`. Three-state semantics matching the existing
  `disallow-main-worktree` and `tool-use-log` precedents.
- Hierarchy merge: scalar override (last-set wins, same as siblings).
- Accessor: `Sweatfile.DisableMergeEnabled() bool`.
- `tommy generate` regenerates `internal/sweatfile/sweatfile_tommy.go`.

Default (unset or `false`) preserves existing behavior.

## Disabled surfaces

When `disable-merge=true`:

### `sc merge` CLI

Early in `merge.Run` (after repo path resolution but before any git
operation), load the sweatfile hierarchy. If `DisableMergeEnabled()`, emit:

```
not ok 1 - merge disabled by sweatfile (disable-merge=true at <path/sweatfile>)
  hint: use `sc check` to run the pre-merge hook without merging
```

Exit non-zero. No git operations performed. The hint teaches agents the
alternate path.

### `merge-this-session` MCP tool

Conditionally omit from registration. In `registerMCPOnlyCommands`
(`cmd/spinclass/commands_mcp_only.go`), call `os.Getwd()`, load the
sweatfile hierarchy, and skip `app.AddCommand` for `merge-this-session`
when disabled. The tool simply does not appear in `tools/list`.

**Risk**: `serve`'s cwd is captured once at startup. If the launch path
does not put cwd inside the worktree, conditional registration sees the
wrong sweatfile. Verify in implementation by reading how Claude Code
launches the server from `.mcp.json`. If unreliable, fall back to
"registered, errors on call" with the same TAP-style message.

`update-this-session-description` stays unconditional. The Claude Code
permission hook in `internal/hooks/hooks.go` is unchanged — it gates
permission tiers, not blocking, and is orthogonal to this feature.

## New `sc check` command

Pre-merge hook is the agent's CI loop. It must run independently.

**Refactor**: extract the existing `runPreMergeHook` body in
`internal/merge/merge.go` into a reusable function (likely a new
`internal/check/` package). It loads the sweatfile hierarchy, runs the
configured `[hooks].pre-merge` command in the worktree, and emits TAP
output the same way merge does today.

### CLI

- `sc check` resolves the current worktree (same logic as `sc merge` cwd
  detection).
- Runs the hook; reports `ok` / `not ok`.
- Exits non-zero if the hook fails.
- No rebase, no merge, no worktree removal.
- Available regardless of `disable-merge` setting.

### `check-this-session` MCP tool

- Registered alongside `merge-this-session` in `registerMCPOnlyCommands`.
- Always registered, regardless of `disable-merge`.
- Wraps `sc check` for agents.
- Annotations: `ReadOnlyHint=false` (the hook may run linters/formatters
  that touch files), `DestructiveHint=false`, `IdempotentHint=false`.

### Naming rationale

The sweatfile key remains `[hooks].pre-merge`. The hook is still
semantically "pre-merge" — that is its contract (gate the merge). `check`
is just an alternate trigger that runs the same hook. No config rename,
no breakage.

## Tests

- `internal/sweatfile/sweatfile_test.go`: parse `disable-merge`, hierarchy
  merge precedence, `DisableMergeEnabled()` accessor.
- `internal/merge/merge_test.go`: case where `disable-merge=true` causes
  `Run` to return the disabled error before any git operation runs;
  assert no `git rebase` / `git merge` / `git worktree remove`.
- `internal/check/`: new package; tests for hook execution, success/failure
  exit codes, TAP output shape.
- `cmd/spinclass/serve_integration_test.go`: sibling to the existing
  `TestServeMergeThisSessionStdioIntegrity` test where the worktree's
  sweatfile has `disable-merge=true`; assert the tool is absent from
  `tools/list`. Add `TestServeCheckThisSession` end-to-end.
- `zz-tests_bats/sweatfile.bats`: bats coverage of the CLI flow.

## Rollback

`disable-merge` is opt-in; default behavior (unset/false) is unchanged.
No dual-architecture period needed.

- User rollback: delete the line from the sweatfile. Single edit.
- Feature rollback: revert the commit. Existing sweatfiles with
  `disable-merge` would parse with an unknown-key warning (depending on
  `tommy` strictness) but would not error.
- `sc check` is purely additive — no rollback concern.

## Documentation

- `cmd/spinclass/doc/spinclass-sweatfile.5`: document the new field.
- `CLAUDE.md` (project): add `sc check` to the CLI Commands table; note
  that `merge-this-session` may be unavailable depending on sweatfile
  config and that agents should fall back to `check-this-session`.
- User CLAUDE.md instructions are out of scope; per-user.

## Out of scope

- Renaming `[hooks].pre-merge` to `[hooks].check`. The current name
  remains the contract.
- Per-branch or per-environment disabling. `disable-merge` is a single
  boolean inherited through the sweatfile hierarchy. Future work could
  add scoping if needed.
- Disabling `sc clean` auto-cleanup. Cleanup keys off worktree state, not
  the merge subsystem; if no merges happen, no cleanup happens.
