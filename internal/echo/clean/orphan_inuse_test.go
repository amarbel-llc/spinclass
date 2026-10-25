package clean

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// #350: a dead-pid .merge-* dir can still be some live process's cwd (a hook
// grandchild that outlived its merge). Deleting it leaves that process in a
// "(deleted)" directory, so the sweep must leave it and say who holds it.
func TestOrphanSweepLeavesADirThatALiveProcessIsIn(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	testgit.MustInit(t, repo)
	dir := filepath.Join(repo, ".worktrees", ".merge-feat-abc1234-999999")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	occupant := exec.Command("sleep", "60")
	occupant.Dir = dir
	if err := occupant.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupant.Process.Kill(); _ = occupant.Wait() }()

	orphans := findOrphanTransientWorktrees(root)
	if len(orphans) != 1 {
		t.Fatalf("want the dead-pid dir listed as an orphan, got %v", orphans)
	}
	err := removeOrphanTransientWorktree(orphans[0])
	if err == nil {
		t.Fatal("removing an occupied orphan must be refused")
	}
	if pid := strconv.Itoa(occupant.Process.Pid); !strings.Contains(err.Error(), pid) {
		t.Errorf("refusal %q should name the occupying pid %s", err, pid)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("occupied orphan dir must survive the sweep: %v", statErr)
	}
}
