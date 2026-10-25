package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// plantedCommondir builds a linked worktree whose <common>/worktrees/<name>/
// commondir file names a repository the worker made. A confined unit binds that
// admin dir read-write, so the file is the worker's to write; git honours it, and
// the planted repository's config names core.fsmonitor, which git runs as a
// command on any index refresh. It returns the worktree and the sentinel the
// command creates.
func plantedCommondir(t *testing.T) (wt, sentinel string) {
	t.Helper()
	testgit.RequireGit(t)
	root := t.TempDir()
	main := filepath.Join(root, "repo")
	testgit.MustInit(t, main)
	testgit.MustWriteFile(t, main, "file.txt", "base\n")
	testgit.MustGit(t, main, "add", "file.txt")
	testgit.MustGit(t, main, "commit", "-m", "base")
	wt = filepath.Join(main, ".worktrees", "x")
	testgit.MustWorktreeAdd(t, main, wt, "x")

	evil := filepath.Join(wt, ".tmp", "evil.git")
	testgit.MustGit(t, root, "clone", "-q", "--bare", main, evil)
	sentinel = filepath.Join(root, "sentinel")
	hook := filepath.Join(root, "fsmonitor-hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+sentinel+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustGit(t, evil, "config", "core.fsmonitor", hook)
	if err := os.WriteFile(filepath.Join(main, ".git", "worktrees", "x", "commondir"), []byte(evil+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return wt, sentinel
}

func assertSentinelAbsent(t *testing.T, sentinel, via string) {
	t.Helper()
	if _, err := os.Stat(sentinel); err == nil {
		t.Errorf("%s ran a command named by a planted commondir's config", via)
		_ = os.Remove(sentinel)
	}
}

// The frontend's git in a linked worktree takes the common dir from the
// worktree's layout (the read-only .git pointer), never from the
// worker-writable commondir file, so a planted repository's config runs nothing.
func TestFrontendGitIgnoresAPlantedCommondir(t *testing.T) {
	wt, sentinel := plantedCommondir(t)

	if _, err := Run(wt, "status", "--porcelain"); err != nil {
		t.Errorf("status: %v", err)
	}
	assertSentinelAbsent(t, sentinel, "Run status")

	StatusPorcelain(wt)
	assertSentinelAbsent(t, sentinel, "StatusPorcelain")

	HasDirtyTracked(wt)
	assertSentinelAbsent(t, sentinel, "HasDirtyTracked")

	if _, err := DirtyTrackedPaths(wt); err != nil {
		t.Errorf("DirtyTrackedPaths: %v", err)
	}
	assertSentinelAbsent(t, sentinel, "DirtyTrackedPaths")

	if _, err := RunEnv(wt, nil, "status", "--porcelain"); err != nil {
		t.Errorf("RunEnv status: %v", err)
	}
	assertSentinelAbsent(t, sentinel, "RunEnv")
}

// The pinned common dir is the real one: git in the worktree still sees the
// main repository's branches, and a subdirectory call resolves the same way.
func TestFrontendGitInAWorktreeStillReadsTheRealRepository(t *testing.T) {
	wt, _ := plantedCommondir(t)
	sub := filepath.Join(wt, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{wt, sub} {
		common, err := CommonGitDir(dir)
		if err != nil {
			t.Fatalf("CommonGitDir(%s): %v", dir, err)
		}
		if want := filepath.Join(filepath.Dir(filepath.Dir(wt)), ".git"); common != want {
			t.Errorf("CommonGitDir(%s) = %s, want the layout's %s", dir, common, want)
		}
		top, err := Run(dir, "rev-parse", "--show-toplevel")
		if err != nil || top != wt {
			t.Errorf("show-toplevel from %s = %q (%v), want %s", dir, top, err, wt)
		}
	}
}

// A main checkout and a plain directory get no pin: their environment is the
// caller's, exactly as before.
func TestCommonDirPinOnlyForLinkedWorktrees(t *testing.T) {
	testgit.RequireGit(t)
	main := filepath.Join(t.TempDir(), "repo")
	testgit.MustInit(t, main)
	if _, env := invocation(main, nil, []string{"status"}); env != nil {
		t.Errorf("main checkout got an env override: %v", env)
	}
	if _, env := invocation(t.TempDir(), nil, []string{"status"}); env != nil {
		t.Errorf("plain directory got an env override: %v", env)
	}

	wt := filepath.Join(main, ".worktrees", "x")
	testgit.MustWorktreeAdd(t, main, wt, "x")
	_, env := invocation(wt, nil, []string{"status"})
	want := "GIT_COMMON_DIR=" + filepath.Join(main, ".git")
	found := false
	for _, e := range env {
		if e == want {
			found = true
		}
		if strings.HasPrefix(e, "GIT_DIR=") || strings.HasPrefix(e, "GIT_WORK_TREE=") {
			t.Errorf("the pin must leave repository discovery alone, got %s", e)
		}
	}
	if !found {
		t.Errorf("linked worktree env lacks %s", want)
	}
}
