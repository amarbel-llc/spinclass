package clean

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// #79: a tombstone is a closed session's record, kept until
// [session-entry].tombstone-retention expires (GCTombstones) because `sc
// resurrect` recreates the session from it (FDR 0027). The abandoned-session
// reap is for live records whose worktree vanished, and must not take a fresh
// tombstone with it.
func TestAbandonedReapLeavesTombstonesToRetention(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	repo := filepath.Join(root, "repo")

	write := func(branch string) string {
		wt := filepath.Join(repo, ".worktrees", branch)
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := session.Write(session.State{
			SessionState: session.StateInactive,
			RepoPath:     repo,
			WorktreePath: wt,
			Branch:       branch,
			SessionKey:   "repo/" + branch,
			StartedAt:    time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		return wt
	}

	// A closed session: tombstoned, worktree removed, as close does.
	closedWt := write("closed-x")
	if err := session.Tombstone(repo, "closed-x", "0123456789abcdef0123456789abcdef01234567"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(closedWt); err != nil {
		t.Fatal(err)
	}
	// A live record whose worktree vanished: genuinely abandoned.
	if err := os.RemoveAll(write("vanished-x")); err != nil {
		t.Fatal(err)
	}

	_, abandoned := countAbandonedSessions()
	var branches []string
	for _, s := range abandoned {
		branches = append(branches, s.Branch)
	}
	if len(abandoned) != 1 || abandoned[0].Branch != "vanished-x" {
		t.Fatalf("abandoned = %v, want only the vanished live record", branches)
	}

	removeAbandonedSessions(abandoned)
	st, err := session.Read(repo, "closed-x")
	if err != nil || !st.IsTombstone() {
		t.Errorf("the fresh tombstone did not survive the abandoned reap: st=%+v err=%v", st, err)
	}
}
