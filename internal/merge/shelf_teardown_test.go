package merge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/store"
	gitstore "code.linenisgreat.com/spinclass/internal/store/git"
)

// The stash shelf lives inside the worktree (FDR 0036 section 4), so a
// non-session merge must not tear a worktree down while the shelf holds
// entries: the commit lands, the worktree and branch stay, and a "keep
// worktree" point says why.
func TestFinishMergeKeepsAShelvedWorktree(t *testing.T) {
	repoDir := setupRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-shelved")

	pinnedSha, _, rep, ts, buf := prepareRacedMerge(t, repoDir, wtPath, "feature-shelved",
		"a.txt", "a", "racing.txt", "race")

	if err := os.WriteFile(filepath.Join(wtPath, "a.txt"), []byte("uncommitted edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitstore.NewShelf().Save(wtPath, store.ShelfSaveRequest{Message: "keep me"}); err != nil {
		t.Fatal(err)
	}

	if _, err := FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature-shelved", "main", pinnedSha, false, false, nil, PostMergeOptions{}); err != nil {
		t.Fatalf("FinishMerge: %v\n%s", err, buf.String())
	}
	ts.Finish()

	if _, statErr := os.Stat(wtPath); statErr != nil {
		t.Errorf("a shelved worktree must survive the merge: %v", statErr)
	}
	if !git.BranchExists(repoDir, "feature-shelved") {
		t.Error("the branch must survive with its worktree")
	}
	if n, err := gitstore.CountShelfEntries(wtPath); err != nil || n != 1 {
		t.Errorf("shelf entries = %d, %v; want 1", n, err)
	}

	const keep = "keep worktree feature-shelved (stash shelf not empty; drop its entries or run sc close)"
	foundKeep := false
	for _, tr := range testRecords(decodeRecords(t, buf.Bytes())) {
		switch tr.Description {
		case keep:
			foundKeep = tr.OK
		case "remove worktree feature-shelved", "delete branch feature-shelved":
			t.Errorf("teardown ran despite a shelved worktree: %+v", tr)
		}
	}
	if !foundKeep {
		t.Errorf("expected a passing %q record", keep)
	}
}
