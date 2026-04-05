# Start From PR Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Add `--pr` flag to `spinclass start` that creates a worktree session from a GitHub PR's branch.

**Architecture:** New `internal/pr/` package handles parsing PR identifiers (number or URL) and calling `gh pr view --json` to get metadata. The `startCmd` in `main.go` checks the `--pr` flag and constructs a `ResolvedPath` with `ExistingBranch` set to the PR's head branch (fetching it first if needed). All downstream behavior (worktree creation, session state, sweatfile) is unchanged.

**Tech Stack:** Go, `gh` CLI (shelled out via `os/exec`), `encoding/json`

**Rollback:** N/A — purely additive feature behind a new flag.

---

### Task 1: Create `internal/pr/` package with URL parsing

**Promotion criteria:** N/A

**Files:**
- Create: `packages/spinclass/internal/pr/pr.go`
- Test: `packages/spinclass/internal/pr/pr_test.go`

**Step 1: Write the failing tests for `ParseIdentifier`**

`ParseIdentifier` takes a string and returns `(owner, repo, number, error)`. It handles:
- Bare number: `"42"` → `("", "", 42, nil)` — owner/repo empty means "use git remote"
- Full URL: `"https://github.com/owner/repo/pull/42"` → `("owner", "repo", 42, nil)`
- Invalid: `"not-a-number"` → error

```go
// packages/spinclass/internal/pr/pr_test.go
package pr

import "testing"

func TestParseIdentifierBareNumber(t *testing.T) {
	owner, repo, number, err := ParseIdentifier("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "" || repo != "" {
		t.Errorf("expected empty owner/repo for bare number, got %q/%q", owner, repo)
	}
	if number != 42 {
		t.Errorf("number = %d, want 42", number)
	}
}

func TestParseIdentifierURL(t *testing.T) {
	owner, repo, number, err := ParseIdentifier("https://github.com/myorg/myrepo/pull/123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "myorg" {
		t.Errorf("owner = %q, want %q", owner, "myorg")
	}
	if repo != "myrepo" {
		t.Errorf("repo = %q, want %q", repo, "myrepo")
	}
	if number != 123 {
		t.Errorf("number = %d, want 123", number)
	}
}

func TestParseIdentifierURLTrailingSlash(t *testing.T) {
	owner, repo, number, err := ParseIdentifier("https://github.com/myorg/myrepo/pull/7/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "myorg" || repo != "myrepo" || number != 7 {
		t.Errorf("got (%q, %q, %d), want (myorg, myrepo, 7)", owner, repo, number)
	}
}

func TestParseIdentifierInvalid(t *testing.T) {
	_, _, _, err := ParseIdentifier("not-a-number")
	if err == nil {
		t.Error("expected error for invalid identifier")
	}
}

func TestParseIdentifierBadURL(t *testing.T) {
	_, _, _, err := ParseIdentifier("https://github.com/owner/repo/issues/42")
	if err == nil {
		t.Error("expected error for non-PR URL")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `nix develop --command go test -run TestParseIdentifier ./packages/spinclass/internal/pr/...`
Expected: Compilation failure — package doesn't exist yet.

**Step 3: Implement `ParseIdentifier`**

```go
// packages/spinclass/internal/pr/pr.go
package pr

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
)

// PRInfo holds metadata about a pull request.
type PRInfo struct {
	HeadRefName       string `json:"headRefName"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	Title             string `json:"title"`
	Number            int    `json:"number"`
}

// ParseIdentifier parses a PR identifier string into owner, repo, and number.
// For bare numbers (e.g. "42"), owner and repo are empty strings.
// For GitHub URLs (e.g. "https://github.com/owner/repo/pull/42"), all three
// are populated.
func ParseIdentifier(identifier string) (owner, repo string, number int, err error) {
	// Try bare number first.
	if n, parseErr := strconv.Atoi(identifier); parseErr == nil {
		return "", "", n, nil
	}

	// Try URL.
	u, parseErr := url.Parse(identifier)
	if parseErr != nil || u.Host != "github.com" {
		return "", "", 0, fmt.Errorf("invalid PR identifier %q: must be a number or github.com URL", identifier)
	}

	// Expected path: /owner/repo/pull/number[/]
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", "", 0, fmt.Errorf("invalid GitHub PR URL %q: expected /owner/repo/pull/number", identifier)
	}

	n, parseErr := strconv.Atoi(parts[3])
	if parseErr != nil {
		return "", "", 0, fmt.Errorf("invalid PR number in URL %q: %w", identifier, parseErr)
	}

	return parts[0], parts[1], n, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `nix develop --command go test -run TestParseIdentifier ./packages/spinclass/internal/pr/...`
Expected: All 5 tests PASS.

**Step 5: Commit**

```
git add packages/spinclass/internal/pr/pr.go packages/spinclass/internal/pr/pr_test.go
git commit -m "feat(spinclass): add PR identifier parsing in internal/pr"
```

---

### Task 2: Add `Resolve` function that calls `gh pr view`

**Promotion criteria:** N/A

**Files:**
- Modify: `packages/spinclass/internal/pr/pr.go`
- Test: `packages/spinclass/internal/pr/pr_test.go`

**Step 1: Write the failing test for `Resolve`**

We can't easily test `gh` calls in unit tests, but we can test the JSON parsing
by adding a `resolveWithRunner` that accepts a command-runner function. The
public `Resolve` wires in `exec.Command`.

```go
// Add to pr_test.go
func TestResolveParsesPRJSON(t *testing.T) {
	jsonResponse := `{
		"headRefName": "fix-login",
		"isCrossRepository": false,
		"title": "Fix login bug",
		"number": 42
	}`

	runner := func(name string, args ...string) ([]byte, error) {
		return []byte(jsonResponse), nil
	}

	info, err := resolveWithRunner("42", "/tmp/repo", runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.HeadRefName != "fix-login" {
		t.Errorf("HeadRefName = %q, want %q", info.HeadRefName, "fix-login")
	}
	if info.IsCrossRepository {
		t.Error("IsCrossRepository should be false")
	}
	if info.Title != "Fix login bug" {
		t.Errorf("Title = %q, want %q", info.Title, "Fix login bug")
	}
	if info.Number != 42 {
		t.Errorf("Number = %d, want 42", info.Number)
	}
}

func TestResolveForkPRReturnsError(t *testing.T) {
	jsonResponse := `{
		"headRefName": "fork-branch",
		"isCrossRepository": true,
		"title": "Fork contribution",
		"number": 99
	}`

	runner := func(name string, args ...string) ([]byte, error) {
		return []byte(jsonResponse), nil
	}

	_, err := resolveWithRunner("99", "/tmp/repo", runner)
	if err == nil {
		t.Fatal("expected error for fork PR")
	}
	if !strings.Contains(err.Error(), "fork") {
		t.Errorf("error should mention fork, got: %v", err)
	}
}

func TestResolveGhNotInstalled(t *testing.T) {
	runner := func(name string, args ...string) ([]byte, error) {
		return nil, &exec.Error{Name: "gh", Err: exec.ErrNotFound}
	}

	_, err := resolveWithRunner("42", "/tmp/repo", runner)
	if err == nil {
		t.Fatal("expected error when gh not found")
	}
	if !strings.Contains(err.Error(), "gh") {
		t.Errorf("error should mention gh, got: %v", err)
	}
}
```

Add `"strings"` and `"os/exec"` to the test imports.

**Step 2: Run tests to verify they fail**

Run: `nix develop --command go test -run TestResolve ./packages/spinclass/internal/pr/...`
Expected: Compilation failure — `resolveWithRunner` doesn't exist.

**Step 3: Implement `Resolve` and `resolveWithRunner`**

Add to `pr.go`:

```go
type commandRunner func(name string, args ...string) ([]byte, error)

func defaultRunner(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		if execErr, ok := err.(*exec.Error); ok {
			return nil, fmt.Errorf("%s is not installed: %w (install from https://cli.github.com)", execErr.Name, err)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh pr view failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

func resolveWithRunner(identifier, repoPath string, runner commandRunner) (PRInfo, error) {
	owner, repo, number, err := ParseIdentifier(identifier)
	if err != nil {
		return PRInfo{}, err
	}

	args := []string{
		"pr", "view", strconv.Itoa(number),
		"--json", "headRefName,isCrossRepository,title,number",
	}
	if owner != "" && repo != "" {
		args = append(args, "--repo", owner+"/"+repo)
	} else {
		args = append(args, "--repo", remoteRepo(repoPath))
	}

	out, err := runner("gh", args...)
	if err != nil {
		return PRInfo{}, err
	}

	var info PRInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return PRInfo{}, fmt.Errorf("parsing gh output: %w", err)
	}

	if info.IsCrossRepository {
		return PRInfo{}, fmt.Errorf(
			"fork PRs are not yet supported (PR #%d is from a fork); fetch the branch manually",
			info.Number,
		)
	}

	return info, nil
}

// Resolve fetches PR metadata from GitHub using the gh CLI.
// identifier is either a bare number ("42") or a GitHub PR URL.
// repoPath is the local git repo path, used to derive the remote when
// identifier is a bare number.
func Resolve(identifier, repoPath string) (PRInfo, error) {
	return resolveWithRunner(identifier, repoPath, defaultRunner)
}

func remoteRepo(repoPath string) string {
	cmd := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return repoSlugFromRemoteURL(strings.TrimSpace(string(out)))
}

func repoSlugFromRemoteURL(remoteURL string) string {
	// Handle SSH: git@github.com:owner/repo.git
	if strings.HasPrefix(remoteURL, "git@") {
		parts := strings.SplitN(remoteURL, ":", 2)
		if len(parts) == 2 {
			return strings.TrimSuffix(parts[1], ".git")
		}
	}
	// Handle HTTPS: https://github.com/owner/repo.git
	u, err := url.Parse(remoteURL)
	if err != nil {
		return remoteURL
	}
	return strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
}
```

**Step 4: Run tests to verify they pass**

Run: `nix develop --command go test -run TestResolve ./packages/spinclass/internal/pr/...`
Expected: All 3 tests PASS.

**Step 5: Commit**

```
git add packages/spinclass/internal/pr/pr.go packages/spinclass/internal/pr/pr_test.go
git commit -m "feat(spinclass): add PR metadata resolution via gh CLI"
```

---

### Task 3: Add `remoteRepo` helper tests

**Promotion criteria:** N/A

**Files:**
- Test: `packages/spinclass/internal/pr/pr_test.go`

**Step 1: Write tests for `repoSlugFromRemoteURL`**

```go
// Add to pr_test.go
func TestRepoSlugFromRemoteURLSSH(t *testing.T) {
	got := repoSlugFromRemoteURL("git@github.com:myorg/myrepo.git")
	if got != "myorg/myrepo" {
		t.Errorf("got %q, want %q", got, "myorg/myrepo")
	}
}

func TestRepoSlugFromRemoteURLHTTPS(t *testing.T) {
	got := repoSlugFromRemoteURL("https://github.com/myorg/myrepo.git")
	if got != "myorg/myrepo" {
		t.Errorf("got %q, want %q", got, "myorg/myrepo")
	}
}

func TestRepoSlugFromRemoteURLNoSuffix(t *testing.T) {
	got := repoSlugFromRemoteURL("https://github.com/myorg/myrepo")
	if got != "myorg/myrepo" {
		t.Errorf("got %q, want %q", got, "myorg/myrepo")
	}
}
```

**Step 2: Run tests to verify they pass**

Run: `nix develop --command go test -run TestRepoSlug ./packages/spinclass/internal/pr/...`
Expected: All 3 tests PASS (implementation already exists from Task 2).

**Step 3: Commit**

```
git add packages/spinclass/internal/pr/pr_test.go
git commit -m "test(spinclass): add remote URL slug parsing tests"
```

---

### Task 4: Wire `--pr` flag into `startCmd`

**Promotion criteria:** N/A

**Files:**
- Modify: `packages/spinclass/cmd/spinclass/main.go:32-38` (add flag var)
- Modify: `packages/spinclass/cmd/spinclass/main.go:46-98` (startCmd RunE)
- Modify: `packages/spinclass/cmd/spinclass/main.go:436-461` (init, flag registration)

**Step 1: Add the `--pr` flag variable and import**

In the `var` block at line 32, add `startPR string`.

In the imports at line 3, add `"github.com/amarbel-llc/spinclass/internal/pr"`.

**Step 2: Modify `startCmd.RunE` to handle `--pr`**

After `repoPath` is resolved (line 65) and before `worktree.ResolvePath` (line 67), add a branch:

```go
		var resolvedPath worktree.ResolvedPath

		if startPR != "" {
			prInfo, err := pr.Resolve(startPR, repoPath)
			if err != nil {
				return err
			}

			branch := prInfo.HeadRefName

			// Fetch the branch if not local.
			if !git.BranchExists(repoPath, branch) {
				if _, err := git.Run(repoPath, "fetch", "origin", branch); err != nil {
					return fmt.Errorf("fetching PR branch %q: %w", branch, err)
				}
			}

			absPath := filepath.Join(repoPath, worktree.WorktreesDir, branch)
			repoDirname := filepath.Base(repoPath)

			description := fmt.Sprintf("%s (#%d)", prInfo.Title, prInfo.Number)
			if len(args) > 0 {
				description = strings.Join(args, " ")
			}

			resolvedPath = worktree.ResolvedPath{
				AbsPath:        absPath,
				RepoPath:       repoPath,
				SessionKey:     repoDirname + "/" + branch,
				Branch:         branch,
				Description:    description,
				ExistingBranch: branch,
			}
		} else {
			var err error
			resolvedPath, err = worktree.ResolvePath(repoPath, args)
			if err != nil {
				return err
			}
		}
```

Remove the old `resolvedPath, err := worktree.ResolvePath(repoPath, args)` call (line 67-70).

**Step 3: Register the flag in `init()`**

After `startNoAttach` flag registration (line 457-461), add:

```go
	startCmd.Flags().StringVar(
		&startPR,
		"pr",
		"",
		"start session from a PR (number or GitHub URL)",
	)
```

**Step 4: Verify it compiles**

Run: `nix develop --command go build ./packages/spinclass/cmd/spinclass/`
Expected: Successful compilation, no errors.

**Step 5: Commit**

```
git add packages/spinclass/cmd/spinclass/main.go
git commit -m "feat(spinclass): wire --pr flag into start command"
```

---

### Task 5: Update CLAUDE.md CLI commands table

**Promotion criteria:** N/A

**Files:**
- Modify: `packages/spinclass/CLAUDE.md:92` (CLI Commands table)

**Step 1: Update the `sc start` entry**

Change line 92 from:
```
  `sc start [desc...]`       Create and start a new worktree session
```
to:
```
  `sc start [desc...]`       Create and start a new worktree session (--pr N or --pr URL)
```

**Step 2: Commit**

```
git add packages/spinclass/CLAUDE.md
git commit -m "docs(spinclass): document --pr flag in CLI commands table"
```
