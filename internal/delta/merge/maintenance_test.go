package merge

import (
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// A merge fixture's commits, rebase and fast-forward must not detach auto
// maintenance: it would keep writing into the fixture's TempDir after the test
// returns and fail its RemoveAll with "directory not empty" (#423).
func TestMergeFixtureGitLeavesNoDetachedMaintenance(t *testing.T) {
	testgit.RequireNoDetachedMaintenance(t)
	repoDir := setupRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-maintenance")
	if err := os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "new.txt")
	runGit(t, wtPath, "commit", "-m", "add new file")

	if _, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-maintenance", "main", false, false); err != nil {
		t.Fatalf("Resolved() error: %v", err)
	}
}
