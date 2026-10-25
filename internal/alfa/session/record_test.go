package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// addSecondSession writes another session under the same repo and index as s.
func addSecondSession(t *testing.T, s State, branch, principal string) State {
	t.Helper()
	wt := filepath.Join(s.RepoPath, ".worktrees", branch)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	other := s
	other.WorktreePath = wt
	other.Branch = branch
	other.SessionKey = filepath.Base(s.RepoPath) + "/" + branch
	other.Principal = principal
	if err := Write(other); err != nil {
		t.Fatal(err)
	}
	return other
}

// twoSessions writes a worker session W1 and a victim session S2 in one repo
// under a fake XDG_STATE_HOME, and returns both.
func twoSessions(t *testing.T) (w1, s2 State) {
	t.Helper()
	w1 = setupTestSession(t, "w1")
	if err := Write(w1); err != nil {
		t.Fatal(err)
	}
	s2 = addSecondSession(t, w1, "victim", "victim-principal")
	return w1, s2
}

// forgeDisplayCopy overwrites wt's worktree-local state.json the way a confined
// worker (which binds its worktree read-write) can.
func forgeDisplayCopy(t *testing.T, wt string, forged State) {
	t.Helper()
	data, err := json.MarshalIndent(forged, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worktreeStatePath(wt), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// forgedVictimCopy is S2's identity plus attacker-chosen authority fields.
func forgedVictimCopy(s2 State) State {
	forged := s2
	forged.Principal = "evil-principal"
	forged.Holders = []string{"evil-principal"}
	forged.PendingHandles = []string{"evil-principal"}
	forged.Entry = &PinnedEntry{Start: []string{"/bin/sh", "-c", "touch forged-entry"}}
	return forged
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A worker rewrites its own state.json to name another session. Everything the
// frontend resolves for the worker's key must still be the worker's own record.
func TestForgedWorktreeCopyDoesNotRedirectRead(t *testing.T) {
	w1, s2 := twoSessions(t)
	forgeDisplayCopy(t, w1.WorktreePath, forgedVictimCopy(s2))

	got, err := Read(w1.RepoPath, w1.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "w1" || got.WorktreePath != w1.WorktreePath {
		t.Errorf("Read(w1) = %s (%s), want the worker's own record", got.Branch, got.WorktreePath)
	}
	if got.Principal == "evil-principal" || len(got.Holders) != 0 || got.Entry != nil {
		t.Errorf("Read(w1) trusted the forged copy: principal=%q holders=%v entry=%v", got.Principal, got.Holders, got.Entry)
	}

	all, err := ListAll(nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, s := range all {
		seen[s.Branch]++
		if s.Principal == "evil-principal" {
			t.Errorf("ListAll returned forged principal on %s", s.Branch)
		}
	}
	if seen["victim"] != 1 || seen["w1"] != 1 {
		t.Errorf("ListAll branches = %v, want exactly one victim and one w1", seen)
	}
}

// What `sc clean` and the close paths do with a listed state: act on its
// RepoPath/Branch. Acting on the worker's state must not reach the victim.
func TestForgedWorktreeCopyCannotRemoveAnotherSession(t *testing.T) {
	w1, s2 := twoSessions(t)
	// The worker's copy names the victim's repo and branch but a worktree that
	// does not exist, so it reads as abandoned.
	forged := forgedVictimCopy(s2)
	forged.WorktreePath = filepath.Join(w1.RepoPath, ".worktrees", "gone")
	forgeDisplayCopy(t, w1.WorktreePath, forged)
	victimDisplay := mustReadFile(t, worktreeStatePath(s2.WorktreePath))

	// removeAbandonedSessions in miniature.
	all, err := ListAll(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if s.ResolveState() == StateAbandoned {
			if err := Remove(s.RepoPath, s.Branch); err != nil {
				t.Fatal(err)
			}
		}
	}
	after, err := os.ReadFile(worktreeStatePath(s2.WorktreePath))
	if err != nil || !bytes.Equal(after, victimDisplay) {
		t.Errorf("victim's worktree copy changed or vanished: err=%v", err)
	}
	if _, err := os.Lstat(indexPath(s2.WorktreePath)); err != nil {
		t.Errorf("victim's index entry is gone: %v", err)
	}
	if _, err := Read(s2.RepoPath, s2.Branch); err != nil {
		t.Errorf("victim's record is gone: %v", err)
	}
}

// Accepting or granting a handle writes the state it was given. The state of a
// worker whose copy was forged must not land on the victim's files.
func TestWritingForgedWorkerStateDoesNotTouchVictim(t *testing.T) {
	w1, s2 := twoSessions(t)
	forgeDisplayCopy(t, w1.WorktreePath, forgedVictimCopy(s2))
	victimDisplay := mustReadFile(t, worktreeStatePath(s2.WorktreePath))

	st, err := Read(w1.RepoPath, w1.Branch)
	if err != nil {
		t.Fatal(err)
	}
	st.GrantHandle("granted-principal", "")
	if err := Write(*st); err != nil {
		t.Fatal(err)
	}

	after := mustReadFile(t, worktreeStatePath(s2.WorktreePath))
	if !bytes.Equal(after, victimDisplay) {
		t.Errorf("victim's worktree copy was rewritten by the worker's handle write:\n%s", after)
	}
	victim, err := Read(s2.RepoPath, s2.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if victim.IsPendingHolder("granted-principal") || victim.IsPendingHolder("evil-principal") {
		t.Errorf("victim record picked up the worker's pending handles: %v", victim.PendingHandles)
	}
}

// Tombstoning the worker closes the worker, whatever its copy claims.
func TestForgedWorktreeCopyCannotTombstoneAnotherSession(t *testing.T) {
	w1, s2 := twoSessions(t)
	forgeDisplayCopy(t, w1.WorktreePath, forgedVictimCopy(s2))

	if err := Tombstone(w1.RepoPath, w1.Branch, "deadbeef"); err != nil {
		t.Fatal(err)
	}
	victim, err := Read(s2.RepoPath, s2.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if victim.IsTombstone() {
		t.Error("victim became a tombstone")
	}
	if got, err := Read(w1.RepoPath, w1.Branch); err != nil || !got.IsTombstone() || got.Branch != "w1" {
		t.Errorf("worker tombstone = %+v, %v; want the worker's own tombstone", got, err)
	}
	if info, err := os.Lstat(indexPath(w1.WorktreePath)); err != nil || !info.Mode().IsRegular() {
		t.Errorf("worker index entry should now be a regular tombstone file: %v", err)
	}
}

// A planted symlink at the worktree-local lock path must not be followed.
func TestUpdateLockDoesNotFollowPlantedSymlink(t *testing.T) {
	w1, _ := twoSessions(t)
	// A dangling target: opening the lock with O_CREATE through the link would
	// create the sentinel file.
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	lock := worktreeStatePath(w1.WorktreePath) + ".lock"
	if err := os.Symlink(sentinel, lock); err != nil {
		t.Fatal(err)
	}
	if err := Update(w1.RepoPath, w1.Branch, func(s *State) error {
		s.Description = "updated"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sentinel); err == nil {
		t.Error("the frontend created the sentinel through the planted lock symlink")
	}
}
