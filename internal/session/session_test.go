package session

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setupTestSession creates a tempdir-rooted "repo" with a worktree and
// returns a State pointing at the (real, on-disk) paths. The XDG_STATE_HOME
// is also rooted under tempdir so the central index is isolated per test.
func setupTestSession(t *testing.T, branch string) State {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(repo, ".worktrees", branch)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	return State{
		PID:          12345,
		SessionState: StateActive,
		RepoPath:     repo,
		WorktreePath: worktree,
		Branch:       branch,
		SessionKey:   filepath.Base(repo) + "/" + branch,
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC().Truncate(time.Second),
	}
}

func TestSessionStateRoundTrip(t *testing.T) {
	s := setupTestSession(t, "my-branch")

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

func TestWriteCreatesIndexSymlink(t *testing.T) {
	s := setupTestSession(t, "feature-x")
	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	idx := indexPath(s.WorktreePath)
	info, err := os.Lstat(idx)
	if err != nil {
		t.Fatalf("index entry not created: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected symlink at %s, got mode %v", idx, info.Mode())
	}

	target, err := os.Readlink(idx)
	if err != nil {
		t.Fatal(err)
	}
	want := worktreeStatePath(s.WorktreePath)
	if target != want {
		t.Errorf("symlink target = %s, want %s", target, want)
	}
}

func TestWriteRequiresWorktreeOnDisk(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := State{
		RepoPath:     "/tmp/no-such-repo",
		WorktreePath: "/tmp/no-such-repo/.worktrees/missing",
		Branch:       "missing",
	}
	err := Write(s)
	if err == nil {
		t.Fatal("expected error writing state for missing worktree")
	}
}

func TestSessionStateRemove(t *testing.T) {
	s := setupTestSession(t, "test")

	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	if err := Remove(s.RepoPath, s.Branch); err != nil {
		t.Fatal(err)
	}

	if _, err := Read(s.RepoPath, s.Branch); err == nil {
		t.Error("expected error reading removed state")
	}
	if _, err := os.Lstat(indexPath(s.WorktreePath)); err == nil {
		t.Error("index entry still present after Remove")
	}
}

func TestRemoveToleratesMissingFiles(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := Remove("/tmp/no-such-repo", "no-such-branch"); err != nil {
		t.Errorf("Remove on missing files should not error, got %v", err)
	}
}

func TestTombstonePromotesSymlinkToRegularFile(t *testing.T) {
	s := setupTestSession(t, "to-be-closed")
	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	if err := Tombstone(s.RepoPath, s.Branch); err != nil {
		t.Fatal(err)
	}

	idx := indexPath(s.WorktreePath)
	info, err := os.Lstat(idx)
	if err != nil {
		t.Fatalf("tombstone missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("expected regular file at %s, still a symlink", idx)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("tombstone is not a regular file: %v", info.Mode())
	}

	// Worktree-local state.json should be gone.
	if _, err := os.Stat(worktreeStatePath(s.WorktreePath)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("worktree state.json should be gone, stat err = %v", err)
	}
	// .spinclass/ dir should be gone too.
	if _, err := os.Stat(filepath.Join(s.WorktreePath, ".spinclass")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".spinclass dir should be gone, stat err = %v", err)
	}

	// Tombstone JSON content should match the original state.
	data, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	var tomb State
	if err := json.Unmarshal(data, &tomb); err != nil {
		t.Fatal(err)
	}
	if tomb.SessionKey != s.SessionKey {
		t.Errorf("tombstone SessionKey = %s, want %s", tomb.SessionKey, s.SessionKey)
	}
}

func TestReadFallsBackToTombstone(t *testing.T) {
	s := setupTestSession(t, "tomb-readback")
	if err := Write(s); err != nil {
		t.Fatal(err)
	}
	if err := Tombstone(s.RepoPath, s.Branch); err != nil {
		t.Fatal(err)
	}

	loaded, err := Read(s.RepoPath, s.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.isTombstone {
		t.Error("expected isTombstone to be true on read-back")
	}
	if loaded.ResolveState() != StateAbandoned {
		t.Errorf("tombstone ResolveState = %s, want abandoned", loaded.ResolveState())
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

func TestResolveStateRunningDetachedReturnsAsIs(t *testing.T) {
	wt := t.TempDir()
	s := &State{
		// PID 0 — running-detached implies the spinclass entrypoint
		// already exited; the multiplexer group's liveness was verified
		// separately by the probe.
		PID:          0,
		SessionState: StateRunningDetached,
		WorktreePath: wt,
	}
	if got := s.ResolveState(); got != StateRunningDetached {
		t.Errorf("ResolveState() = %s, want running-detached", got)
	}
}

func TestGCTombstones(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	repo := filepath.Join(base, "repo")

	mk := func(branch string, exited time.Time) State {
		t.Helper()
		wt := filepath.Join(repo, ".worktrees", branch)
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		s := State{
			PID:          12345,
			SessionState: StateActive,
			RepoPath:     repo,
			WorktreePath: wt,
			Branch:       branch,
			SessionKey:   "repo/" + branch,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    exited.Add(-time.Hour),
			ExitedAt:     &exited,
		}
		return s
	}

	// Three sessions: one stale tombstone, one fresh tombstone, one live.
	stale := mk("stale", time.Now().Add(-48*time.Hour))
	fresh := mk("fresh", time.Now().Add(-1*time.Hour))
	live := mk("live", time.Now())
	live.ExitedAt = nil

	for _, s := range []State{stale, fresh, live} {
		if err := Write(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := Tombstone(stale.RepoPath, stale.Branch); err != nil {
		t.Fatal(err)
	}
	if err := Tombstone(fresh.RepoPath, fresh.Branch); err != nil {
		t.Fatal(err)
	}

	// 24h retention → stale (48h ago) gets reaped, fresh (1h ago) stays.
	removed, err := GCTombstones(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("GCTombstones removed %d, want 1", removed)
	}

	// Stale entry's index file should be gone.
	if _, err := os.Lstat(indexPath(stale.WorktreePath)); err == nil {
		t.Error("stale tombstone should have been removed")
	}
	// Fresh entry's tombstone should still be present.
	if _, err := os.Lstat(indexPath(fresh.WorktreePath)); err != nil {
		t.Errorf("fresh tombstone should still exist: %v", err)
	}
	// Live session's symlink should be untouched.
	info, err := os.Lstat(indexPath(live.WorktreePath))
	if err != nil {
		t.Fatalf("live session index entry missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("live session entry should still be a symlink")
	}
}

func TestGCTombstonesZeroRetentionIsNoOp(t *testing.T) {
	s := setupTestSession(t, "noop")
	if err := Write(s); err != nil {
		t.Fatal(err)
	}
	if err := Tombstone(s.RepoPath, s.Branch); err != nil {
		t.Fatal(err)
	}
	removed, err := GCTombstones(0)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("GCTombstones(0) removed %d, want 0", removed)
	}
	if _, err := os.Lstat(indexPath(s.WorktreePath)); err != nil {
		t.Error("tombstone should still exist after retention=0 GC")
	}
}

func TestSortStatesPlacesDetachedBetweenActiveAndInactive(t *testing.T) {
	live := t.TempDir()
	states := []State{
		{Branch: "z-inactive", WorktreePath: live, SessionState: StateInactive},
		{Branch: "a-detached", WorktreePath: live, SessionState: StateRunningDetached},
		{Branch: "b-active", WorktreePath: live, SessionState: StateActive, PID: os.Getpid()},
		{Branch: "c-detached", WorktreePath: live, SessionState: StateRunningDetached},
	}
	SortStates(states)

	want := []string{"b-active", "a-detached", "c-detached", "z-inactive"}
	for i, b := range want {
		if states[i].Branch != b {
			t.Errorf("[%d] = %q, want %q (got order: %v)", i, states[i].Branch, b, branches(states))
			return
		}
	}
}

func TestListAllEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	states, err := ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Errorf("expected 0 states, got %d", len(states))
	}
}

func TestListAllClassifiesEntryShapes(t *testing.T) {
	// Build three states under the same XDG_STATE_HOME: one live, one
	// tombstoned, one with a dangling symlink (worktree gone).
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	repo := filepath.Join(base, "repo")

	mkSession := func(branch string) State {
		t.Helper()
		wt := filepath.Join(repo, ".worktrees", branch)
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		return State{
			PID:          12345,
			SessionState: StateActive,
			RepoPath:     repo,
			WorktreePath: wt,
			Branch:       branch,
			SessionKey:   "repo/" + branch,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC().Truncate(time.Second),
		}
	}

	live := mkSession("alive")
	if err := Write(live); err != nil {
		t.Fatal(err)
	}

	tomb := mkSession("closed")
	if err := Write(tomb); err != nil {
		t.Fatal(err)
	}
	if err := Tombstone(tomb.RepoPath, tomb.Branch); err != nil {
		t.Fatal(err)
	}

	dangling := mkSession("ghost")
	if err := Write(dangling); err != nil {
		t.Fatal(err)
	}
	// Externally remove the worktree without going through Remove.
	if err := os.RemoveAll(dangling.WorktreePath); err != nil {
		t.Fatal(err)
	}

	states, err := ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(states), branches(states))
	}

	got := map[string]string{}
	for _, s := range states {
		got[s.Branch] = s.ResolveState()
	}
	if got["alive"] != StateActive && got["alive"] != StateInactive {
		t.Errorf("live entry resolved to %q, want active/inactive", got["alive"])
	}
	if got["closed"] != StateAbandoned {
		t.Errorf("tombstone entry resolved to %q, want abandoned", got["closed"])
	}
	if got["ghost"] != StateAbandoned {
		t.Errorf("dangling entry resolved to %q, want abandoned", got["ghost"])
	}
}

func TestFindByWorktreePath(t *testing.T) {
	s := setupTestSession(t, "plain-spruce")
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
	subdir := filepath.Join(s.WorktreePath, "src")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	found, err = FindByWorktreePath(subdir)
	if err != nil {
		t.Fatal(err)
	}
	if found.SessionKey != s.SessionKey {
		t.Errorf("subdirectory: SessionKey = %s, want %s", found.SessionKey, s.SessionKey)
	}

	// No match
	if _, err = FindByWorktreePath("/completely/different/path"); err == nil {
		t.Error("expected error for non-matching path")
	}
}

func TestFindByID(t *testing.T) {
	s := setupTestSession(t, "plain-spruce")
	s.Branch = "different-branch"
	s.SessionKey = "repo/different-branch"
	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	// Match by worktree directory name, not branch.
	found, err := FindByID("plain-spruce")
	if err != nil {
		t.Fatal(err)
	}
	if found.WorktreePath != s.WorktreePath {
		t.Errorf("WorktreePath = %s, want %s", found.WorktreePath, s.WorktreePath)
	}

	if _, err = FindByID("nonexistent"); err == nil {
		t.Error("expected error for non-matching ID")
	}
}

// TestFindByWorktreePathRejectsSiblingWithSamePrefix guards against the
// pre-2026-04 bug where strings.HasPrefix matched /foo/bar against
// /foo/bar-baz. Component-aware matching must reject the sibling.
func TestFindByWorktreePathRejectsSiblingWithSamePrefix(t *testing.T) {
	s := setupTestSession(t, "feature")
	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	sibling := filepath.Join(filepath.Dir(s.WorktreePath), "feature-bar", "src")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := FindByWorktreePath(sibling); err == nil {
		t.Error("FindByWorktreePath matched feature against feature-bar")
	}
}

// TestFindByWorktreePathThroughSymlink confirms a symlinked cwd matches
// the real worktree path stored in state.
func TestFindByWorktreePathThroughSymlink(t *testing.T) {
	s := setupTestSession(t, "feature")
	if err := os.MkdirAll(filepath.Join(s.WorktreePath, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(s.RepoPath), "link")
	if err := os.Symlink(s.WorktreePath, link); err != nil {
		t.Fatal(err)
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
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	repo := filepath.Join(base, "repo")

	for _, branch := range []string{"feature-a", "feature-b"} {
		wt := filepath.Join(repo, ".worktrees", branch)
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		s := State{
			PID:          12345,
			SessionState: StateActive,
			RepoPath:     repo,
			WorktreePath: wt,
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

// ============================================================================
// Migration tests
// ============================================================================

// writeLegacy dumps a state JSON in the pre-slice-1 layout for migration tests.
func writeLegacy(t *testing.T, s State) {
	t.Helper()
	dir := legacyStateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(s.RepoPath + "/" + s.Branch))
	name := fmt.Sprintf("%x-state.json", h[:8])
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateMovesLegacyEntries(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	repo := filepath.Join(base, "repo")
	wt := filepath.Join(repo, ".worktrees", "old-feature")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}

	s := State{
		PID:          7777,
		SessionState: StateActive,
		RepoPath:     repo,
		WorktreePath: wt,
		Branch:       "old-feature",
		SessionKey:   "repo/old-feature",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC().Truncate(time.Second),
	}
	writeLegacy(t, s)

	if err := MigrateNow(); err != nil {
		t.Fatal(err)
	}

	// New layout artifacts present.
	if _, err := os.Stat(worktreeStatePath(wt)); err != nil {
		t.Errorf("worktree state.json missing post-migration: %v", err)
	}
	info, err := os.Lstat(indexPath(wt))
	if err != nil {
		t.Errorf("index symlink missing: %v", err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("index entry is not a symlink: %v", info.Mode())
	}

	// Legacy artifacts gone.
	if _, err := os.Stat(legacyStateDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("legacy dir should be removed when empty, stat err = %v", err)
	}
}

func TestMigrateDropsAbandonedEntries(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))

	// Worktree path that doesn't exist on disk.
	s := State{
		PID:          1,
		SessionState: StateActive,
		RepoPath:     "/no/such/repo",
		WorktreePath: "/no/such/repo/.worktrees/ghost",
		Branch:       "ghost",
		SessionKey:   "repo/ghost",
		StartedAt:    time.Now().UTC(),
	}
	writeLegacy(t, s)

	if err := MigrateNow(); err != nil {
		t.Fatal(err)
	}

	// No index entry should exist for the abandoned session.
	if _, err := os.Lstat(indexPath(s.WorktreePath)); err == nil {
		t.Error("abandoned session should not get an index entry")
	}
	// Legacy file should be gone.
	if _, err := os.Stat(legacyStatePath(s.RepoPath, s.Branch)); !errors.Is(err, os.ErrNotExist) {
		t.Error("abandoned legacy file should be removed")
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	repo := filepath.Join(base, "repo")
	wt := filepath.Join(repo, ".worktrees", "f")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLegacy(t, State{
		PID: 1, SessionState: StateActive,
		RepoPath: repo, WorktreePath: wt, Branch: "f", SessionKey: "repo/f",
		StartedAt: time.Now().UTC(),
	})

	if err := MigrateNow(); err != nil {
		t.Fatal(err)
	}
	// Second run should be a no-op (legacy dir is now gone).
	if err := MigrateNow(); err != nil {
		t.Errorf("second migration errored: %v", err)
	}

	// Verify the new artifacts are still intact.
	if _, err := os.Stat(worktreeStatePath(wt)); err != nil {
		t.Errorf("worktree state.json gone after second migration: %v", err)
	}
}
