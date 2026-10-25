package clean

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// A confined worker rewrites its worktree-local state.json to name a sibling
// session's repo and branch with a worktree that does not exist, so `sc clean`
// would read it as abandoned and remove the sibling's state. The sibling must be
// untouched, and the worker (whose worktree exists) is not abandoned at all.
func TestRemoveAbandonedSessionsIgnoresAForgedWorktreeRecord(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	repo := filepath.Join(root, "repo")

	mk := func(branch string) session.State {
		wt := filepath.Join(repo, ".worktrees", branch)
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		st := session.State{
			SessionState: session.StateInactive,
			RepoPath:     repo,
			WorktreePath: wt,
			Branch:       branch,
			SessionKey:   "repo/" + branch,
			StartedAt:    time.Now().UTC(),
		}
		if err := session.Write(st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	w1, victim := mk("w1"), mk("victim")

	forged := victim
	forged.WorktreePath = filepath.Join(repo, ".worktrees", "vanished")
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w1.WorktreePath, ".spinclass", "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, abandoned := countAbandonedSessions()
	removeAbandonedSessions(abandoned)

	if _, err := session.Read(repo, "victim"); err != nil {
		t.Errorf("the victim's session record was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(victim.WorktreePath, ".spinclass", "state.json")); err != nil {
		t.Errorf("the victim's worktree copy was removed: %v", err)
	}
	if _, err := session.Read(repo, "w1"); err != nil {
		t.Errorf("the worker's own record was removed: %v", err)
	}
}
