package shop

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/testgit"
	"code.linenisgreat.com/spinclass/internal/worktree"
	tap "code.linenisgreat.com/tap/go/pkgs/writer"
	"code.linenisgreat.com/tap/go/pkgs/yaml_diagnostic"
)

// TestMain sandboxes $HOME once for the package. Several tests reach
// claude.TrustWorkspace, which mkdirs under HOME — fails inside the
// nix sandbox where the default HOME (/homeless-shelter) is read-only.
// It also isolates every git invocation from the host git configuration
// (signing agent, hooks, templates) — see testgit.SetHermeticEnv.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "shop-test-home-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", home)
	gitCleanup, err := testgit.SetHermeticEnv()
	if err != nil {
		panic(err)
	}
	code := m.Run()
	gitCleanup()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

func TestStatusDescription(t *testing.T) {
	tests := []struct {
		name          string
		defaultBranch string
		commitsAhead  int
		porcelain     string
		want          string
	}{
		{
			name:          "ahead and clean",
			defaultBranch: "master",
			commitsAhead:  3,
			porcelain:     "",
			want:          "3 commits ahead of master, clean",
		},
		{
			name:          "one commit ahead",
			defaultBranch: "master",
			commitsAhead:  1,
			porcelain:     "",
			want:          "1 commit ahead of master, clean",
		},
		{
			name:          "ahead and dirty",
			defaultBranch: "main",
			commitsAhead:  2,
			porcelain:     "M file.go\n",
			want:          "2 commits ahead of main, dirty",
		},
		{
			name:          "merged",
			defaultBranch: "master",
			commitsAhead:  0,
			porcelain:     "",
			want:          "0 commits ahead of master, clean, (merged)",
		},
		{
			name:          "zero ahead but dirty",
			defaultBranch: "master",
			commitsAhead:  0,
			porcelain:     "?? untracked\n",
			want:          "0 commits ahead of master, dirty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statusDescription(tt.defaultBranch, tt.commitsAhead, tt.porcelain)
			if got != tt.want {
				t.Errorf("statusDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCreateTapNewWorktreeErrorPath(t *testing.T) {
	// Simulate a spinclass-like environment: a parent repo with a worktree,
	// and a separate non-git directory. GIT_CEILING_DIRECTORIES prevents
	// git from discovering repos above the test root, so the non-git dir
	// is truly isolated regardless of where TMPDIR points.
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit(repoDir, "init")
	runGit(repoDir, "config", "user.email", "test@test.com")
	runGit(repoDir, "config", "user.name", "Test")
	runGit(repoDir, "commit", "--allow-empty", "-m", "initial")

	nonGitDir := filepath.Join(root, "non-git")
	if err := os.MkdirAll(nonGitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	worktreePath := filepath.Join(nonGitDir, "new-worktree")

	rp := worktree.ResolvedPath{
		AbsPath:  worktreePath,
		RepoPath: nonGitDir,
		Branch:   "feature-x",
	}

	var buf bytes.Buffer
	_, err := Create(&buf, rp, false, "tap", nil)
	if err == nil {
		t.Error("expected error when creating worktree in non-git dir, got nil")
	}
}

func TestCreateTapSkipExisting(t *testing.T) {
	dir := t.TempDir()

	rp := worktree.ResolvedPath{
		AbsPath:  dir,
		RepoPath: dir,
		Branch:   "feature-x",
	}

	var buf bytes.Buffer
	if _, err := Create(&buf, rp, false, "tap", nil); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "# SKIP already exists") {
		t.Errorf("expected SKIP line, got: %q", got)
	}
}

func TestCreateTapNewWorktree(t *testing.T) {
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	runGit("commit", "--allow-empty", "-m", "initial")

	worktreePath := filepath.Join(parentDir, "spinclass-test-wt")

	rp := worktree.ResolvedPath{
		AbsPath:  worktreePath,
		RepoPath: repoDir,
		Branch:   "feature-wt",
	}

	var buf bytes.Buffer
	if _, err := Create(&buf, rp, false, "tap", nil); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "ok 1 - create") {
		t.Errorf("expected ok line, got: %q", got)
	}
	if !strings.Contains(got, "1..1") {
		t.Errorf("expected plan line 1..1, got: %q", got)
	}
}

func TestCreateSharedWriter(t *testing.T) {
	dir := t.TempDir()

	rp := worktree.ResolvedPath{
		AbsPath:  dir,
		RepoPath: dir,
		Branch:   "feature-x",
	}

	var buf bytes.Buffer
	tw := tap.NewWriter(&buf)
	tw.PlanAhead(2)

	if _, err := Create(&buf, rp, false, "tap", tw); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	got := buf.String()
	// Should use the shared writer's counter (test point 1)
	if !strings.Contains(got, "ok 1 - create feature-x # SKIP") {
		t.Errorf("expected ok 1 with SKIP, got: %q", got)
	}
	// Should NOT contain a second "TAP version 14" line
	if strings.Count(got, "TAP version 14") != 1 {
		t.Errorf("expected exactly one TAP version line, got: %q", got)
	}
}

type mockExecutor struct {
	attachCalled bool
	attachDir    string
	attachKey    string
	attachCmd    []string
	detachCalled bool
}

func (m *mockExecutor) Attach(dir string, key string, command []string, dryRun bool, tp *tap.TestPoint) error {
	m.attachCalled = true
	m.attachDir = dir
	m.attachKey = key
	m.attachCmd = command
	if dryRun {
		tp.Skip = "dry run"
		tp.Diagnostics = &yaml_diagnostic.YAMLDiagnostic{
			Extras: map[string]any{"command": "mock-command"},
		}
	}
	return nil
}

func (m *mockExecutor) Detach() error {
	m.detachCalled = true
	return nil
}

func TestNewTapExistingWorktree(t *testing.T) {
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit(repoDir, "init")
	runGit(repoDir, "config", "user.email", "test@test.com")
	runGit(repoDir, "config", "user.name", "Test")
	runGit(repoDir, "commit", "--allow-empty", "-m", "initial")

	// Create worktree so attach finds it existing
	wtDir := filepath.Join(repoDir, worktree.WorktreesDir)
	wtPath := filepath.Join(wtDir, "feature-tap")
	runGit(repoDir, "worktree", "add", "-b", "feature-tap", wtPath)

	rp := worktree.ResolvedPath{
		AbsPath:    wtPath,
		RepoPath:   repoDir,
		Branch:     "feature-tap",
		SessionKey: "repo/feature-tap",
	}

	mock := &mockExecutor{}
	var buf bytes.Buffer
	err := Attach(&buf, mock, rp, sweatfile.Sweatfile{}, "tap", false, false, false)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if !mock.attachCalled {
		t.Error("expected executor.Attach to be called")
	}

	got := buf.String()

	// Single TAP version line
	if strings.Count(got, "TAP version 14") != 1 {
		t.Errorf("expected exactly one TAP version line, got: %q", got)
	}

	// Plan
	if !strings.Contains(got, "1..3") {
		t.Errorf("expected plan 1..3, got: %q", got)
	}

	// Pull test point (no upstream -> SKIP)
	if !strings.Contains(got, "ok 1 - pull repo # SKIP no upstream") {
		t.Errorf("expected pull SKIP test point, got: %q", got)
	}

	// Create test point (existing worktree -> SKIP)
	if !strings.Contains(got, "ok 2 - create feature-tap # SKIP") {
		t.Errorf("expected create SKIP test point, got: %q", got)
	}

	// Close test point
	if !strings.Contains(got, "ok 3 - close feature-tap") {
		t.Errorf("expected close test point, got: %q", got)
	}
}

func TestNewNoAttach(t *testing.T) {
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit(repoDir, "init")
	runGit(repoDir, "config", "user.email", "test@test.com")
	runGit(repoDir, "config", "user.name", "Test")
	runGit(repoDir, "commit", "--allow-empty", "-m", "initial")

	wtDir := filepath.Join(repoDir, worktree.WorktreesDir)
	wtPath := filepath.Join(wtDir, "feature-dry")
	runGit(repoDir, "worktree", "add", "-b", "feature-dry", wtPath)

	rp := worktree.ResolvedPath{
		AbsPath:    wtPath,
		RepoPath:   repoDir,
		Branch:     "feature-dry",
		SessionKey: "repo/feature-dry",
	}

	mock := &mockExecutor{}
	var buf bytes.Buffer
	err := Attach(&buf, mock, rp, sweatfile.Sweatfile{}, "tap", true, true, false)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	got := buf.String()

	// Single TAP version line
	if strings.Count(got, "TAP version 14") != 1 {
		t.Errorf("expected exactly one TAP version line, got: %q", got)
	}

	// Pull test point (no upstream -> SKIP)
	if !strings.Contains(got, "ok 1 - pull repo # SKIP no upstream") {
		t.Errorf("expected pull SKIP test point, got: %q", got)
	}

	// Create test point (existing worktree -> SKIP)
	if !strings.Contains(got, "ok 2 - create feature-dry # SKIP") {
		t.Errorf("expected create SKIP test point, got: %q", got)
	}

	// Attach test point (dry run -> SKIP with command diagnostic)
	if !strings.Contains(got, "ok 3 - attach feature-dry # SKIP dry run") {
		t.Errorf("expected attach SKIP test point, got: %q", got)
	}
	if !strings.Contains(got, "command: mock-command") {
		t.Errorf("expected command diagnostic, got: %q", got)
	}

	// Trailing plan
	if !strings.Contains(got, "1..3") {
		t.Errorf("expected plan 1..3, got: %q", got)
	}

	// --no-attach now writes findable session state (inactive, PID 0) so a
	// later `sc exec`/merge/close can target it (the create-then-operate
	// workflow `sc run` builds on). It used to write nothing.
	st, err := session.Read(repoDir, "feature-dry")
	if err != nil {
		t.Fatalf("expected --no-attach to write session state, got error: %v", err)
	}
	if st.PID != 0 {
		t.Errorf("--no-attach state PID = %d, want 0", st.PID)
	}
	if st.SessionState != session.StateInactive {
		t.Errorf("--no-attach state = %q, want %q", st.SessionState, session.StateInactive)
	}
}

func TestForkAutoName(t *testing.T) {
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit(repoDir, "init")
	runGit(repoDir, "config", "user.email", "test@test.com")
	runGit(repoDir, "config", "user.name", "Test")
	runGit(repoDir, "commit", "--allow-empty", "-m", "initial")

	// Create the source worktree inside .worktrees/
	wtDir := filepath.Join(repoDir, worktree.WorktreesDir)
	srcPath := filepath.Join(wtDir, "source-branch")
	runGit(repoDir, "worktree", "add", "-b", "source-branch", srcPath)

	rp := worktree.ResolvedPath{
		AbsPath:  srcPath,
		RepoPath: repoDir,
		Branch:   "source-branch",
	}

	var buf bytes.Buffer
	err := Fork(&buf, rp, "", "tap")
	if err != nil {
		t.Fatalf("Fork() error: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "ok 1 - fork source-branch-1") {
		t.Errorf("expected ok line with fork name, got: %q", got)
	}

	// Forked worktree should exist
	forkedPath := filepath.Join(wtDir, "source-branch-1")
	if _, err := os.Stat(forkedPath); os.IsNotExist(err) {
		t.Errorf("expected forked worktree at %s", forkedPath)
	}
}

func TestAttachCallsExecutorWithCorrectArgs(t *testing.T) {
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit(repoDir, "init")
	runGit(repoDir, "config", "user.email", "test@test.com")
	runGit(repoDir, "config", "user.name", "Test")
	runGit(repoDir, "commit", "--allow-empty", "-m", "initial")

	wtDir := filepath.Join(repoDir, worktree.WorktreesDir)
	wtPath := filepath.Join(wtDir, "feature-attach")
	runGit(repoDir, "worktree", "add", "-b", "feature-attach", wtPath)

	rp := worktree.ResolvedPath{
		AbsPath:    wtPath,
		RepoPath:   repoDir,
		Branch:     "feature-attach",
		SessionKey: "myrepo/feature-attach",
	}

	mock := &mockExecutor{}
	var buf bytes.Buffer
	err := Attach(&buf, mock, rp, sweatfile.Sweatfile{}, "tap", false, true, false)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if !mock.attachCalled {
		t.Fatal("expected executor.Attach to be called")
	}
	if mock.attachDir != wtPath {
		t.Errorf("Attach dir = %q, want %q", mock.attachDir, wtPath)
	}
	if mock.attachKey != "myrepo/feature-attach" {
		t.Errorf("Attach key = %q, want %q", mock.attachKey, "myrepo/feature-attach")
	}
}

// TestAttachPreservesFieldsItDoesNotOwn pins #147: the pre-attach state
// write must carry over fields owned by other flows — FDR 0006 lineage
// (spawned_by, hello_sent_at) and a buffered pre-merge attestation — instead
// of constructing a fresh State that silently drops them on every resume.
func TestAttachPreservesFieldsItDoesNotOwn(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit(repoDir, "init")
	runGit(repoDir, "config", "user.email", "test@test.com")
	runGit(repoDir, "config", "user.name", "Test")
	runGit(repoDir, "commit", "--allow-empty", "-m", "initial")

	wtPath := filepath.Join(repoDir, worktree.WorktreesDir, "spawned-worker")
	runGit(repoDir, "worktree", "add", "-b", "spawned-worker", wtPath)
	// Keep the worktree dirty so closeShop's auto-close gate (clean +
	// nothing ahead) never fires and removes the state under assertion.
	if err := os.WriteFile(filepath.Join(wtPath, "wip.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}

	hello := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	prior := session.State{
		PID:          0,
		SessionState: session.StateActive,
		RepoPath:     repoDir,
		WorktreePath: wtPath,
		Branch:       "spawned-worker",
		SessionKey:   "myrepo/spawned-worker",
		SpawnedBy:    "myrepo/driver",
		HelloSentAt:  &hello,
		PreMergeAttestation: &session.PreMergeAttestation{
			RecordedAt: hello,
			Skills:     []session.AttestedSkill{{Name: "review", Used: true, Reasoning: "buffered"}},
		},
	}
	if err := session.Write(prior); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	rp := worktree.ResolvedPath{
		AbsPath:    wtPath,
		RepoPath:   repoDir,
		Branch:     "spawned-worker",
		SessionKey: "myrepo/spawned-worker",
	}
	mock := &mockExecutor{}
	var buf bytes.Buffer
	if err := Attach(&buf, mock, rp, sweatfile.Sweatfile{}, "tap", false, false, false); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	got, err := session.Read(repoDir, "spawned-worker")
	if err != nil {
		t.Fatalf("session.Read after attach: %v", err)
	}
	if got.SpawnedBy != "myrepo/driver" {
		t.Errorf("SpawnedBy = %q, want %q (clobbered by attach)", got.SpawnedBy, "myrepo/driver")
	}
	if got.HelloSentAt == nil || !got.HelloSentAt.Equal(hello) {
		t.Errorf("HelloSentAt = %v, want %v (clobbered by attach)", got.HelloSentAt, hello)
	}
	if got.PreMergeAttestation == nil || len(got.PreMergeAttestation.Skills) != 1 {
		t.Errorf("PreMergeAttestation = %+v, want the buffered attestation preserved", got.PreMergeAttestation)
	}
}

func TestNewMergeOnCloseCleanWorktree(t *testing.T) {
	parentDir := t.TempDir()
	t.Setenv("HOME", parentDir)
	repoDir := filepath.Join(parentDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// `-b main` so DefaultBranch resolves deterministically inside the
	// sandbox (where there's no user gitconfig to set init.defaultBranch).
	runGit(repoDir, "init", "-b", "main")
	runGit(repoDir, "config", "user.email", "test@test.com")
	runGit(repoDir, "config", "user.name", "Test")
	runGit(repoDir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(repoDir, "add", "file.txt")
	runGit(repoDir, "commit", "-m", "initial")

	wtDir := filepath.Join(repoDir, worktree.WorktreesDir)
	wtPath := filepath.Join(wtDir, "feature-moc")
	runGit(repoDir, "worktree", "add", "-b", "feature-moc", wtPath)

	// Mirror production setup: spinclass-managed worktrees get
	// `.spinclass/` written into `.git/info/exclude` by
	// `worktree.Create` -> `applyGitExcludes`. This test bypasses that
	// path (it pre-stages the worktree via raw `git worktree add` so a
	// feature commit can be added before Attach runs), so the porcelain
	// status check inside closeShop would otherwise see the session-
	// state directory as untracked and mark the worktree dirty. On the
	// host the user's global core.excludesFile masks this; inside the
	// nix sandbox there is no global excludesFile, so the test fails
	// unless we install the exclude ourselves. Tracked under #65.
	excludePath := filepath.Join(repoDir, ".git", "info", "exclude")
	if err := os.WriteFile(excludePath, []byte(".spinclass/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Add a commit on the feature branch so merge has something to do
	if err := os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(wtPath, "add", "new.txt")
	runGit(wtPath, "commit", "-m", "feature commit")

	rp := worktree.ResolvedPath{
		AbsPath:    wtPath,
		RepoPath:   repoDir,
		Branch:     "feature-moc",
		SessionKey: "repo/feature-moc",
	}

	mock := &mockExecutor{}
	var buf bytes.Buffer

	// mergeOnClose=true, noAttach=false (Attach returns immediately from mock)
	err := Attach(&buf, mock, rp, sweatfile.Sweatfile{}, "tap", true, false, false)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	// Worktree should have been merged and removed
	if _, statErr := os.Stat(wtPath); !os.IsNotExist(statErr) {
		t.Error("expected worktree to be removed after merge-on-close")
	}

	// Commit should be on main
	out, _ := exec.Command("git", "-C", repoDir, "log", "--oneline").CombinedOutput()
	if !strings.Contains(string(out), "feature commit") {
		t.Errorf("expected feature commit on main, got: %s", string(out))
	}
}

func TestDiscardAllCleansWorktree(t *testing.T) {
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit(repoDir, "init")
	runGit(repoDir, "config", "user.email", "test@test.com")
	runGit(repoDir, "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(repoDir, "add", "tracked.txt")
	runGit(repoDir, "commit", "-m", "initial")

	wtDir := filepath.Join(repoDir, worktree.WorktreesDir)
	wtPath := filepath.Join(wtDir, "dirty-test")
	runGit(repoDir, "worktree", "add", "-b", "dirty-test", wtPath)

	// Dirty it: modify tracked file + add untracked file
	if err := os.WriteFile(filepath.Join(wtPath, "tracked.txt"), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "untracked.txt"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	if git.StatusPorcelain(wtPath) == "" {
		t.Fatal("expected dirty worktree")
	}

	if err := discardAll(wtPath); err != nil {
		t.Fatalf("discardAll error: %v", err)
	}

	if status := git.StatusPorcelain(wtPath); status != "" {
		t.Errorf("expected clean worktree after discard, got: %q", status)
	}
}
