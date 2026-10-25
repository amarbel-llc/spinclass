package check

import (
	"os"
	"path/filepath"
	"testing"
)

// writeWorktreeOwnSweatfile writes the worktree's OWN sweatfile (the file a
// worker can write), as opposed to writeSweatfile, which writes the main
// checkout's.
func writeWorktreeOwnSweatfile(t *testing.T, wtPath, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wtPath, "sweatfile"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// `sc check` runs the main checkout's pre-merge hook and never a command named
// in the worktree's own sweatfile.
func TestRunIgnoresWorktreeSweatfileHook(t *testing.T) {
	root, _, wtPath := setupRepoWithWorktree(t, "feature-wt-layer")
	repoHook := filepath.Join(root, "repo-hook-ran")
	sentinel := filepath.Join(root, "SENTINEL")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"touch "+repoHook+"\"\ndisable-merge-build-worktree = true\n")
	writeWorktreeOwnSweatfile(t, wtPath, "[hooks]\npre-merge = \"touch "+sentinel+"\"\n")

	if _, _, err := runCheck(t, wtPath); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("the worktree sweatfile's pre-merge hook ran")
	}
	if _, err := os.Stat(repoHook); err != nil {
		t.Errorf("the main checkout's pre-merge hook did not run: %v", err)
	}
}

// A corrupt worktree sweatfile neither fails the check nor displaces the hook.
func TestRunIgnoresCorruptWorktreeSweatfile(t *testing.T) {
	root, _, wtPath := setupRepoWithWorktree(t, "feature-wt-corrupt")
	repoHook := filepath.Join(root, "repo-hook-ran")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"touch "+repoHook+"\"\ndisable-merge-build-worktree = true\n")
	writeWorktreeOwnSweatfile(t, wtPath, "[hooks\n=== not toml\n")

	if _, _, err := runCheck(t, wtPath); err != nil {
		t.Fatalf("a corrupt worktree sweatfile failed the check: %v", err)
	}
	if _, err := os.Stat(repoHook); err != nil {
		t.Errorf("the main checkout's pre-merge hook did not run: %v", err)
	}
}
