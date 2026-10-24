package proccwd

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func startSleeperIn(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

func TestInsideFindsAProcessWhoseCwdIsBelowDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := startSleeperIn(t, sub)

	if got := Inside(dir); !slices.Contains(got, pid) {
		t.Fatalf("Inside(%s) = %v, want it to contain %d", dir, got, pid)
	}
}

func TestInsideIgnoresASiblingWithASharedNamePrefix(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	sibling := filepath.Join(root, "x-other")
	for _, d := range []string{dir, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pid := startSleeperIn(t, sibling)

	if got := Inside(dir); slices.Contains(got, pid) {
		t.Fatalf("Inside(%s) = %v, must not contain %d (cwd %s)", dir, got, pid, sibling)
	}
}

func TestInsideOfAMissingDirIsEmpty(t *testing.T) {
	if got := Inside(filepath.Join(t.TempDir(), "gone")); len(got) != 0 {
		t.Fatalf("Inside(missing) = %v, want none", got)
	}
}
