package session

import (
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
