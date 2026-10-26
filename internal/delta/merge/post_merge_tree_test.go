package merge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readPostMergeWhere reads the "<pwd>\n<HEAD>\n" a post-merge hook recorded.
func readPostMergeWhere(t *testing.T, path string) (dir, head string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("post-merge did not run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("post-merge record = %q, want pwd and HEAD", raw)
	}
	return lines[0], lines[1]
}

// #340: with no landing worktree (a local-only merge that needed no landing
// rebase), post-merge used to run in the live session worktree, so a commit
// made there during the merge was what it ran against. It now runs in a
// detached checkout of the landed sha, as a pushing merge does.
func TestPostMergeRunsAtTheLandedShaNotTheLiveSessionWorktree(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	where := filepath.Join(t.TempDir(), "where")
	writeRepoSweatfile(t, repoDir, fmt.Sprintf(
		"[hooks]\npost-merge = \"pwd > %s; git rev-parse HEAD >> %s\"\n", where, where))

	recs, err := runFinishWithMidEdit(t, repoDir, wtPath, "feature", false, PostMergeOptions{}, func() {
		writeFile(t, wtPath, "later.txt", "after the pin")
		runGit(t, wtPath, "add", "later.txt")
		runGit(t, wtPath, "commit", "-m", "after the pin")
	})
	if err != nil {
		t.Fatalf("merge: %v (points %v)", err, testDescs(testRecords(recs)))
	}
	landed := runGit(t, repoDir, "rev-parse", "main")
	dir, head := readPostMergeWhere(t, where)

	if head != landed {
		t.Errorf("post-merge ran at %s, want the landed %s", head, landed)
	}
	if live, _ := filepath.EvalSymlinks(wtPath); dir == live {
		t.Errorf("post-merge ran in the live session worktree %s", dir)
	}
	if !strings.HasPrefix(filepath.Base(dir), LandWorktreePrefix) {
		t.Errorf("post-merge ran in %s, want a %s* checkout", dir, LandWorktreePrefix)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("the post-merge checkout %s was left behind (stat err %v)", dir, statErr)
	}
}
