package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

func TestResolvePathArgsAreDescription(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repos", "myrepo")

	rp, err := ResolvePath(repoPath, []string{"fixing", "the", "login", "bug"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rp.Description != "fixing the login bug" {
		t.Errorf("Description = %q, want %q", rp.Description, "fixing the login bug")
	}
	// Branch should be random, not derived from args
	if rp.Branch == "" {
		t.Error("Branch should not be empty")
	}
	if rp.RepoPath != repoPath {
		t.Errorf("RepoPath = %q, want %q", rp.RepoPath, repoPath)
	}
}

func TestResolvePathAlwaysRandomBranch(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repos", "myrepo")

	rp, err := ResolvePath(repoPath, []string{"feature-x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Branch should be a random name, not "feature_x"
	if rp.Branch == "feature_x" || rp.Branch == "feature-x" {
		t.Errorf("Branch should be random, got %q", rp.Branch)
	}
	wantKey := "myrepo/" + rp.Branch
	if rp.SessionKey != wantKey {
		t.Errorf("SessionKey = %q, want %q", rp.SessionKey, wantKey)
	}
}

func TestDetectRepoFindsGitDir(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "myrepo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(repoDir, "src", "pkg")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := DetectRepo(subDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != repoDir {
		t.Errorf("DetectRepo() = %q, want %q", got, repoDir)
	}
}

func TestDetectRepoSkipsGitFile(t *testing.T) {
	root := t.TempDir()
	// Create a parent repo with a .git directory
	parentRepo := filepath.Join(root, "parent")
	if err := os.MkdirAll(filepath.Join(parentRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a worktree-like child with a .git file (not directory)
	child := filepath.Join(parentRepo, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, ".git"), []byte("gitdir: ../parent/.git/worktrees/child"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DetectRepo(child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != parentRepo {
		t.Errorf(
			"DetectRepo() = %q, want %q (should skip .git file and find parent)",
			got,
			parentRepo,
		)
	}
}

func TestDetectRepoFailsOutsideRepo(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	dir := filepath.Join(root, "no-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := DetectRepo(dir)
	if err == nil {
		t.Error("expected error when no git repo found, got nil")
	}
}

func TestScanReposFromRepo(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "myrepo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, WorktreesDir), 0o755); err != nil {
		t.Fatal(err)
	}

	repos := ScanRepos(repoDir)
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if repos[0] != repoDir {
		t.Errorf("repos[0] = %q, want %q", repos[0], repoDir)
	}
}

func TestScanReposFromParent(t *testing.T) {
	root := t.TempDir()

	// Create two repos with WorktreesDir
	for _, name := range []string{"repo-a", "repo-b"} {
		repoDir := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(repoDir, WorktreesDir), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Create a repo without WorktreesDir (should be excluded)
	noWtRepo := filepath.Join(root, "repo-c")
	if err := os.MkdirAll(filepath.Join(noWtRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	repos := ScanRepos(root)
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d: %v", len(repos), repos)
	}

	found := make(map[string]bool)
	for _, r := range repos {
		found[filepath.Base(r)] = true
	}
	if !found["repo-a"] || !found["repo-b"] {
		t.Errorf("expected repo-a and repo-b, got %v", repos)
	}
}

func TestScanReposEmpty(t *testing.T) {
	root := t.TempDir()

	repos := ScanRepos(root)
	if len(repos) != 0 {
		t.Errorf("expected 0 repos, got %d: %v", len(repos), repos)
	}
}

func TestListWorktrees(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "myrepo")
	wtDir := filepath.Join(repoDir, WorktreesDir)

	branches := []string{"feature-a", "feature-b", "bugfix-1"}
	for _, b := range branches {
		branchDir := filepath.Join(wtDir, b)
		if err := os.MkdirAll(branchDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Create .git file to mark as worktree
		if err := os.WriteFile(filepath.Join(branchDir, ".git"), []byte("gitdir: ../../../.git/worktrees/"+b+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Also create a file (should be excluded)
	if err := os.WriteFile(filepath.Join(wtDir, "not-a-dir"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a plain directory (not a worktree — no .git file)
	if err := os.MkdirAll(filepath.Join(wtDir, "not-a-worktree"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := ListWorktrees(repoDir)
	if len(got) != 3 {
		t.Fatalf("expected 3 worktrees, got %d: %v", len(got), got)
	}

	found := make(map[string]bool)
	for _, wt := range got {
		found[filepath.Base(wt)] = true
		if !filepath.IsAbs(wt) {
			t.Errorf("expected absolute path, got %q", wt)
		}
	}
	for _, b := range branches {
		if !found[b] {
			t.Errorf("missing worktree %q in results %v", b, got)
		}
	}
}

func TestListWorktreesEmpty(t *testing.T) {
	root := t.TempDir()

	got := ListWorktrees(root)
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestIsWorktreeWithGitFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: somewhere"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !IsWorktree(dir) {
		t.Error("expected IsWorktree=true for .git file")
	}
}

func TestIsWorktreeWithGitDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	if IsWorktree(dir) {
		t.Error("expected IsWorktree=false for .git directory")
	}
}

func TestIsWorktreeNoGit(t *testing.T) {
	dir := t.TempDir()

	if IsWorktree(dir) {
		t.Error("expected IsWorktree=false for directory without .git")
	}
}

func TestForkName(t *testing.T) {
	dir := t.TempDir()
	wtDir := filepath.Join(dir, WorktreesDir)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// No collisions: first fork is <branch>-1
	got := ForkName(dir, "my-feature")
	if got != "my-feature-1" {
		t.Errorf("ForkName() = %q, want %q", got, "my-feature-1")
	}

	// Create my-feature-1 dir, next should be my-feature-2
	if err := os.Mkdir(filepath.Join(wtDir, "my-feature-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	got = ForkName(dir, "my-feature")
	if got != "my-feature-2" {
		t.Errorf("ForkName() = %q, want %q", got, "my-feature-2")
	}

	// Create my-feature-2 as well, next should be my-feature-3
	if err := os.Mkdir(filepath.Join(wtDir, "my-feature-2"), 0o755); err != nil {
		t.Fatal(err)
	}
	got = ForkName(dir, "my-feature")
	if got != "my-feature-3" {
		t.Errorf("ForkName() = %q, want %q", got, "my-feature-3")
	}
}

// TestForkNameAvoidsBranchCollision covers the #207-class blind spot in
// ForkName: a lingering <sourceBranch>-N branch (worktree dir gone) must count
// as a collision, so a fork never draws a name that `git worktree add -b` would
// then reject — it skips to the next free suffix instead.
func TestForkNameAvoidsBranchCollision(t *testing.T) {
	testgit.RequireGit(t)
	repoPath := t.TempDir()
	testgit.MustInit(t, repoPath)

	// A branch named my-feature-1 exists with no matching worktree directory.
	if out, err := exec.Command("git", "-C", repoPath, "branch", "my-feature-1").CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}

	// The directory check alone would return my-feature-1; the branch check
	// must skip it to my-feature-2.
	if got := ForkName(repoPath, "my-feature"); got != "my-feature-2" {
		t.Errorf("ForkName() = %q, want %q (must skip the existing branch)", got, "my-feature-2")
	}
}

func TestResolvePathDescriptionFromMultipleArgs(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repos", "myrepo")

	rp, err := ResolvePath(
		repoPath,
		[]string{"this", "is", "the", "description"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rp.Description != "this is the description" {
		t.Errorf("Description = %q, want %q", rp.Description, "this is the description")
	}
	if rp.Branch == "" {
		t.Error("Branch should be a random name, not empty")
	}
}

func TestResolvePathEmptyDescriptionWhenNoArgs(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repos", "myrepo")

	rp, err := ResolvePath(repoPath, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rp.Description != "" {
		t.Errorf("Description = %q, want empty", rp.Description)
	}
}

func TestResolvePathRandomNameWhenNoArgs(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repos", "myrepo")

	rp, err := ResolvePath(repoPath, []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rp.Branch == "" {
		t.Error("expected non-empty random branch name")
	}
	if rp.ExistingBranch != "" {
		t.Errorf(
			"ExistingBranch = %q, want empty for random name",
			rp.ExistingBranch,
		)
	}
}
