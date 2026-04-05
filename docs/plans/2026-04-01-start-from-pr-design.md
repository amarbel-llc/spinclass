# Design: `spinclass start --pr`

## Problem

Starting a worktree session to work on an existing PR requires manually looking
up the branch name, fetching it, and passing it correctly. A `--pr` flag on
`start` streamlines this.

## Approach

Add a `--pr` flag to `start` that accepts a PR number or full GitHub URL,
resolves PR metadata via `gh` CLI, and creates a worktree session on the PR's
head branch.

### PR identifier formats

- Bare number: `--pr 42` (resolves repo from git remote)
- Full URL: `--pr https://github.com/owner/repo/pull/42`

### Metadata resolution

Shell out to `gh pr view <number> --repo <remote> --json
headRefName,isCrossRepository,title,number`. The `--repo` flag is derived from
the git remote when a bare number is provided, or from the URL when a full URL
is given.

### Fork detection

If `isCrossRepository` is true, fail with: "fork PRs are not yet supported,
fetch the branch manually". This avoids the complexity of fetching
`refs/pull/N/head` and creating tracking branches.

### Branch handling

Fetch the branch if not already local (`git fetch origin <branch>`), then create
the worktree with `ExistingBranch` set to the PR's head branch. The worktree
directory name matches the branch name (placed under `.worktrees/`). Pushing
from this worktree updates the PR directly.

### Session description

Default to `<PR title> (#<number>)`. If the user passes positional args after
`--pr`, those override the PR title as the description.

### Data flow

```
--pr 42
  -> pr.Resolve("42", repoPath)
    -> gh pr view 42 --repo <remote> --json headRefName,isCrossRepository,title,number
    -> PRInfo{HeadRefName: "fix-bug", IsCrossRepository: false, Title: "Fix bug", Number: 42}
  -> if IsCrossRepository -> error "fork PRs not yet supported"
  -> if branch not local -> git fetch origin fix-bug
  -> ResolvedPath{Branch: "fix-bug", ExistingBranch: "fix-bug", Description: "Fix bug (#42)"}
  -> shop.Attach(...)
```

### Error cases

- `gh` not installed: clear error with install hint
- PR not found: propagate gh error
- Fork PR: "fork PRs are not yet supported, fetch the branch manually"
- Branch already checked out in another worktree: git's own error propagates

## New code

### `internal/pr/` package

- `PRInfo` struct: `HeadRefName`, `IsCrossRepository`, `Title`, `Number`
- `Resolve(identifier string, repoPath string) (PRInfo, error)`: parses
  identifier (number vs URL), calls `gh pr view`, returns metadata
- URL parsing extracts owner/repo and number from GitHub PR URLs

### Changes to `cmd/spinclass/main.go`

- Add `--pr` string flag to `startCmd`
- When `--pr` is set: resolve PR info, check fork status, fetch branch if
  needed, construct `ResolvedPath` with `ExistingBranch` and PR-derived
  description

## Testing

- Unit tests in `internal/pr/` for URL parsing (number extraction, URL vs bare
  number detection)
- Integration covered by existing `start` flow (`ExistingBranch` path in
  `worktree.Create` is already tested)

## Not in scope

- Fork PR support (future work)
- PR creation from spinclass
- Updating PR metadata from spinclass
