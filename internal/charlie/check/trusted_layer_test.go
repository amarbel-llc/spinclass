package check

import (
	"os"
	"path/filepath"
	"testing"
)

// During `sc check` the REPO layer of the sweatfile hierarchy must come from the
// sweatfile COMMITTED on the default branch tip: never from the session
// branch's own commits and never from the main checkout's live, uncommitted
// <repo>/sweatfile.
func TestRunPreMergeHookComesFromCommittedDefaultBranch(t *testing.T) {
	_, repoDir, wtPath := setupRepoWithWorktree(t, "feature-trusted")
	dir := t.TempDir()
	good := filepath.Join(dir, "GOOD")
	session := filepath.Join(dir, "SESSION")
	live := filepath.Join(dir, "LIVE")
	body := func(marker string) string {
		return "[hooks]\npre-merge = \"touch " + marker + "\"\n"
	}
	write := func(at, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(at, "sweatfile"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(repoDir, body(good))
	runGit(t, repoDir, "add", "sweatfile")
	runGit(t, repoDir, "commit", "-m", "default branch sweatfile")
	write(wtPath, body(session))
	runGit(t, wtPath, "add", "sweatfile")
	runGit(t, wtPath, "commit", "-m", "session sweatfile")
	write(repoDir, body(live))

	if _, _, err := runCheck(t, wtPath); err != nil {
		t.Logf("Run: %v", err)
	}
	if _, err := os.Stat(good); err != nil {
		t.Errorf("the sweatfile committed on the default branch did not run: %v", err)
	}
	if _, err := os.Stat(session); err == nil {
		t.Errorf("a command from the SESSION branch's committed sweatfile ran (%s exists)", session)
	}
	if _, err := os.Stat(live); err == nil {
		t.Errorf("a command from the main checkout's LIVE uncommitted sweatfile ran (%s exists)", live)
	}
}
