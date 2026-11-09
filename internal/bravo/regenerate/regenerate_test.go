package regenerate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commit(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", "commit "+name)
}

// repoWithBase is a repo whose branch `base` holds flake.lock = baseLock (when
// non-empty), with HEAD on a branch cut from it.
func repoWithBase(t *testing.T, baseLock string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "base")
	commit(t, dir, "README", "r")
	if baseLock != "" {
		commit(t, dir, LockFile, baseLock)
	}
	runGit(t, dir, "checkout", "-q", "-b", "work")
	return dir
}

func TestLockChanged(t *testing.T) {
	cases := []struct {
		name     string
		baseLock string
		edit     func(t *testing.T, dir string)
		want     bool
	}{
		{"same lock", "L1", func(t *testing.T, dir string) { commit(t, dir, "x", "x") }, false},
		{"lock edited", "L1", func(t *testing.T, dir string) { commit(t, dir, LockFile, "L2") }, true},
		{"lock added", "", func(t *testing.T, dir string) { commit(t, dir, LockFile, "L1") }, true},
		{"lock absent on both", "", func(t *testing.T, dir string) { commit(t, dir, "x", "x") }, false},
		{"lock removed", "L1", func(t *testing.T, dir string) {
			runGit(t, dir, "rm", "-q", LockFile)
			runGit(t, dir, "commit", "-q", "-m", "drop lock")
		}, true},
		{"edited then restored", "L1", func(t *testing.T, dir string) {
			commit(t, dir, LockFile, "L2")
			commit(t, dir, LockFile, "L1")
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := repoWithBase(t, tc.baseLock)
			tc.edit(t, dir)
			got, err := LockChanged(dir, "base")
			if err != nil {
				t.Fatalf("LockChanged: %v", err)
			}
			if got != tc.want {
				t.Errorf("LockChanged = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLockChangedBadBaseRefIsAnError(t *testing.T) {
	dir := repoWithBase(t, "L1")
	if _, err := LockChanged(dir, "no-such-ref"); err == nil {
		t.Fatal("an unresolvable base must be an error, not a silent no-change")
	}
}

func TestLockChangedDoesNotReadTheWorkingTree(t *testing.T) {
	dir := repoWithBase(t, "L1")
	if err := os.WriteFile(filepath.Join(dir, LockFile), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LockChanged(dir, "base")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("an uncommitted edit must not trigger the phase: it judges committed content")
	}
}
