package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	s := State{
		PID:          12345,
		SessionState: StateActive,
		RepoPath:     "/home/user/repos/bob",
		WorktreePath: "/home/user/repos/bob/.worktrees/my-branch",
		Branch:       "my-branch",
		SessionKey:   "bob/my-branch",
		Entrypoint:   []string{"zellij"},
		Env:          map[string]string{"SPINCLASS_SESSION_ID": "bob/my-branch"},
		StartedAt:    time.Now().UTC().Truncate(time.Second),
	}

	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	loaded, err := Read(s.RepoPath, s.Branch)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.PID != s.PID {
		t.Errorf("PID = %d, want %d", loaded.PID, s.PID)
	}
	if loaded.SessionState != StateActive {
		t.Errorf("State = %s, want active", loaded.SessionState)
	}
	if loaded.SessionKey != s.SessionKey {
		t.Errorf("SessionKey = %s, want %s", loaded.SessionKey, s.SessionKey)
	}
	if !loaded.StartedAt.Equal(s.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", loaded.StartedAt, s.StartedAt)
	}
}

func TestSessionStateHash(t *testing.T) {
	h1 := stateFilename("/home/user/repos/bob", "my-branch")
	h2 := stateFilename("/home/user/repos/bob", "other-branch")
	if h1 == h2 {
		t.Error("different branches should produce different hashes")
	}
	if !strings.HasSuffix(h1, "-state.json") {
		t.Errorf("expected -state.json suffix, got %s", h1)
	}
}

func TestSessionStateRemove(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	s := State{
		PID:          99999,
		SessionState: StateActive,
		RepoPath:     "/tmp/test-repo",
		WorktreePath: "/tmp/test-repo/.worktrees/test",
		Branch:       "test",
		SessionKey:   "test-repo/test",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}

	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	if err := Remove(s.RepoPath, s.Branch); err != nil {
		t.Fatal(err)
	}

	_, err := Read(s.RepoPath, s.Branch)
	if err == nil {
		t.Error("expected error reading removed state file")
	}
}

func TestResolveStateAbandoned(t *testing.T) {
	s := &State{
		PID:          1,
		SessionState: StateActive,
		WorktreePath: "/nonexistent/path/that/does/not/exist",
	}
	if got := s.ResolveState(); got != StateAbandoned {
		t.Errorf("ResolveState() = %s, want abandoned", got)
	}
}

func TestListAllEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	states, err := ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Errorf("expected 0 states, got %d", len(states))
	}
}

func TestFindByWorktreePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	s := State{
		PID:          12345,
		SessionState: StateActive,
		RepoPath:     "/home/user/repos/bob",
		WorktreePath: "/home/user/repos/bob/.worktrees/plain-spruce",
		Branch:       "plain-spruce",
		SessionKey:   "bob/plain-spruce",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}
	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	// Exact match
	found, err := FindByWorktreePath(s.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if found.SessionKey != s.SessionKey {
		t.Errorf("SessionKey = %s, want %s", found.SessionKey, s.SessionKey)
	}

	// Subdirectory match
	found, err = FindByWorktreePath(s.WorktreePath + "/src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if found.SessionKey != s.SessionKey {
		t.Errorf("subdirectory: SessionKey = %s, want %s", found.SessionKey, s.SessionKey)
	}

	// No match
	_, err = FindByWorktreePath("/completely/different/path")
	if err == nil {
		t.Error("expected error for non-matching path")
	}
}

func TestFindByID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	s := State{
		PID:          12345,
		SessionState: StateActive,
		RepoPath:     "/home/user/repos/bob",
		WorktreePath: "/home/user/repos/bob/.worktrees/plain-spruce",
		Branch:       "different-branch",
		SessionKey:   "bob/different-branch",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}
	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	// Match by worktree directory name, not branch
	found, err := FindByID("plain-spruce")
	if err != nil {
		t.Fatal(err)
	}
	if found.WorktreePath != s.WorktreePath {
		t.Errorf("WorktreePath = %s, want %s", found.WorktreePath, s.WorktreePath)
	}

	// No match
	_, err = FindByID("nonexistent")
	if err == nil {
		t.Error("expected error for non-matching ID")
	}
}

// TestFindByWorktreePathRejectsSiblingWithSamePrefix guards against the
// pre-2026-04 bug where strings.HasPrefix matched /foo/bar against
// /foo/bar-baz. Component-aware matching must reject the sibling.
func TestFindByWorktreePathRejectsSiblingWithSamePrefix(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	s := State{
		PID:          12345,
		SessionState: StateActive,
		RepoPath:     "/tmp/repo",
		WorktreePath: "/tmp/repo/.worktrees/feature",
		Branch:       "feature",
		SessionKey:   "repo/feature",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}
	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	if _, err := FindByWorktreePath("/tmp/repo/.worktrees/feature-bar/src"); err == nil {
		t.Error("FindByWorktreePath matched /tmp/repo/.worktrees/feature against /tmp/repo/.worktrees/feature-bar/src")
	}
}

// TestFindByWorktreePathThroughSymlink confirms a symlinked cwd matches
// the real worktree path stored in state. EvalSymlinks runs on both
// sides so /tmp/link → /tmp/repo/.worktrees/feature is found via either
// /tmp/link or /tmp/link/sub.
func TestFindByWorktreePathThroughSymlink(t *testing.T) {
	root := t.TempDir()
	stateRoot := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateRoot)

	real := filepath.Join(root, "repo", ".worktrees", "feature")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	s := State{
		PID:          12345,
		SessionState: StateActive,
		RepoPath:     filepath.Join(root, "repo"),
		WorktreePath: real,
		Branch:       "feature",
		SessionKey:   "repo/feature",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}
	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	if _, err := FindByWorktreePath(link); err != nil {
		t.Errorf("symlink lookup of root: %v", err)
	}
	if _, err := FindByWorktreePath(filepath.Join(link, "sub")); err != nil {
		t.Errorf("symlink lookup of subdir: %v", err)
	}
}

// TestSortStatesActiveFirst confirms active sessions sort before
// inactive ones; ties break alphabetically by branch.
func TestSortStatesActiveFirst(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}

	states := []State{
		{Branch: "zeta", WorktreePath: live, SessionState: StateInactive},
		{Branch: "alpha", WorktreePath: live, SessionState: StateActive, PID: os.Getpid()},
		{Branch: "beta", WorktreePath: live, SessionState: StateActive, PID: os.Getpid()},
		{Branch: "gamma", WorktreePath: live, SessionState: StateInactive},
	}
	SortStates(states)

	want := []string{"alpha", "beta", "gamma", "zeta"}
	for i, b := range want {
		if states[i].Branch != b {
			t.Errorf("[%d] = %q, want %q (got order: %v)", i, states[i].Branch, b, branches(states))
			return
		}
	}
}

func branches(states []State) []string {
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = s.Branch
	}
	return out
}

func TestListAllWithEntries(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	for _, branch := range []string{"feature-a", "feature-b"} {
		s := State{
			PID:          12345,
			SessionState: StateActive,
			RepoPath:     "/tmp/repo",
			WorktreePath: "/tmp/repo/.worktrees/" + branch,
			Branch:       branch,
			SessionKey:   "repo/" + branch,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		}
		if err := Write(s); err != nil {
			t.Fatal(err)
		}
	}

	states, err := ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Errorf("expected 2 states, got %d", len(states))
	}
}
