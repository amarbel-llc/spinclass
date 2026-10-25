package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/store"
)

// Two sessions in one repo. W1's admin dir commondir is planted to name a
// repository whose config runs a core.fsmonitor hook, as a confined worker in
// W1 could do. A grit call on behalf of S2 aimed at W1 must never run git
// there unless S2 holds a handle on W1's session.
func twoSessionWorktrees(t *testing.T) (repo, w1, w2, sentinel string, sessionAt func(string) (string, bool)) {
	t.Helper()
	repo = newRepo(t)
	w1 = filepath.Join(repo, ".worktrees", "w1")
	w2 = filepath.Join(repo, ".worktrees", "w2")
	mustRun(t, repo, "worktree", "add", "-b", "w1", w1)
	mustRun(t, repo, "worktree", "add", "-b", "w2", w2)

	evil := filepath.Join(w1, ".tmp", "evil.git")
	mustRun(t, repo, "clone", "-q", "--bare", repo, evil)
	sentinel = filepath.Join(t.TempDir(), "SENTINEL")
	hook := filepath.Join(t.TempDir(), "fsmonitor-hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+sentinel+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, evil, "config", "core.fsmonitor", hook)
	if err := os.WriteFile(filepath.Join(repo, ".git", "worktrees", "w1", "commondir"), []byte(evil+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	keys := map[string]string{w1: "repo/w1", w2: "repo/w2"}
	sessionAt = func(root string) (string, bool) {
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", false
		}
		for wt, key := range keys {
			if r, err := filepath.EvalSymlinks(wt); err == nil && r == real {
				return key, true
			}
		}
		return "", false
	}
	return repo, w1, w2, sentinel, sessionAt
}

func callerIn(w string, sessionAt func(string) (string, bool), held ...string) store.Session {
	return store.Session{Dir: w, Branch: filepath.Base(w), Confinement: store.ConfinementUnconfined, SessionAt: sessionAt, Held: held}
}

func TestGritRefusesAnotherSessionsWorktreeWithoutAHandle(t *testing.T) {
	_, w1, w2, sentinel, sessionAt := twoSessionWorktrees(t)

	for _, verb := range [][2]string{{"stage", "status"}, {"branch", "log"}, {"stage", "diff"}} {
		_, err := doAs(t, callerIn(w2, sessionAt), verb[0], verb[1], map[string]any{"repo_path": w1})
		if err == nil || !strings.Contains(err.Error(), "repo/w1") || !strings.Contains(err.Error(), "no handle") {
			t.Errorf("%s %s on another session's worktree: err = %v, want a refusal naming repo/w1 and the missing handle", verb[0], verb[1], err)
		}
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("a git call on behalf of S2 ran in W1 (the planted commondir's hook fired)")
	}
}

func TestGritRefusalNamesTheHandlesTheCallerHolds(t *testing.T) {
	_, w1, w2, _, sessionAt := twoSessionWorktrees(t)
	_, err := doAs(t, callerIn(w2, sessionAt, "repo/other"), "stage", "status", map[string]any{"repo_path": w1})
	if err == nil || !strings.Contains(err.Error(), "repo/other") {
		t.Errorf("err = %v, want the refusal to list the handles held (repo/other)", err)
	}
}

func TestGritAllowsAnotherSessionsWorktreeWithAHandle(t *testing.T) {
	_, w1, w2, sentinel, sessionAt := twoSessionWorktrees(t)
	if _, err := doAs(t, callerIn(w2, sessionAt, "repo/w1"), "stage", "status", map[string]any{"repo_path": w1}); err != nil {
		t.Errorf("a handle holder was refused: %v", err)
	}
	// Even when allowed, the commondir pin keeps the planted repository out.
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("the planted commondir's hook fired for a handle holder")
	}
}

func TestGritAllowsTheCallersOwnWorktreeAndPlainRepos(t *testing.T) {
	repo, _, w2, _, sessionAt := twoSessionWorktrees(t)
	if _, err := doAs(t, callerIn(w2, sessionAt), "stage", "status", map[string]any{"repo_path": w2}); err != nil {
		t.Errorf("own worktree refused: %v", err)
	}
	if _, err := doAs(t, callerIn(w2, sessionAt), "stage", "status", map[string]any{"repo_path": repo}); err != nil {
		t.Errorf("the main checkout (no session) refused for an unconfined caller: %v", err)
	}
}

func TestGritRefusesAnUnresolvableRepoPath(t *testing.T) {
	_, _, w2, _, sessionAt := twoSessionWorktrees(t)
	_, err := doAs(t, callerIn(w2, sessionAt), "stage", "status", map[string]any{"repo_path": filepath.Join(t.TempDir(), "gone")})
	if err == nil || !strings.Contains(err.Error(), "cannot resolve") {
		t.Errorf("err = %v, want a fail-closed refusal for an unresolvable repo_path", err)
	}
}

// The PreToolUse reading applies the same scoping before any git runs: a call
// aimed at another session's worktree asks unless the caller holds a handle.
func TestClassifyAsksForAnotherSessionsWorktreeWithoutAHandle(t *testing.T) {
	_, w1, w2, sentinel, sessionAt := twoSessionWorktrees(t)
	for _, tc := range []struct {
		tool, sub string
		args      map[string]any
	}{
		{"stage", "status", map[string]any{"repo_path": w1}},
		{"remote", "fetch", map[string]any{"repo_path": w1}},
		{"branch", "create", map[string]any{"repo_path": w1, "name": "w2--x"}},
	} {
		if got := New().Classify(callerIn(w2, sessionAt), op(t, tc.tool, tc.sub, tc.args)); got != store.Ask {
			t.Errorf("Classify(%s %s) on another session's worktree = %v, want ask", tc.tool, tc.sub, got)
		}
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("Classify ran git in W1 on behalf of S2 (the planted commondir's hook fired)")
	}
	if got := New().Classify(callerIn(w2, sessionAt, "repo/w1"), op(t, "stage", "status", map[string]any{"repo_path": w1})); got != store.Allow {
		t.Errorf("a handle holder's read = %v, want allow", got)
	}
	if got := New().Classify(callerIn(w2, sessionAt), op(t, "stage", "status", map[string]any{"repo_path": w2})); got != store.Allow {
		t.Errorf("a read of the caller's own worktree = %v, want allow", got)
	}
}

// A symlink pointing into another session's worktree is resolved first.
func TestGritRefusesASymlinkIntoAnotherSessionsWorktree(t *testing.T) {
	_, w1, w2, sentinel, sessionAt := twoSessionWorktrees(t)
	link := filepath.Join(t.TempDir(), "into-w1")
	if err := os.Symlink(w1, link); err != nil {
		t.Fatal(err)
	}
	if _, err := doAs(t, callerIn(w2, sessionAt), "stage", "status", map[string]any{"repo_path": link}); err == nil {
		t.Error("a symlink into another session's worktree was not refused")
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("the planted commondir's hook fired through a symlink")
	}
}
