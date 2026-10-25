package session

import (
	"os"
	"path/filepath"
	"testing"
)

// mkLinkedWorktreeDir creates dir as a fixture for a live linked worktree: a
// directory with a .git file, which is what git leaves in one. Without it a
// fixture reads worktree-gone (#409).
func mkLinkedWorktreeDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// #409: a session whose worktree git removed while its agent kept running
// (only .tmp recreated in the old path) must not read "active" in `sc list`:
// its display state is worktree-gone, whatever its PID or clowns say. The base
// ResolveState is unchanged, so close and the list filter treat it as before.
func TestResolveDisplayStateMarksAWorktreeGitRemoved(t *testing.T) {
	repo := t.TempDir()
	gone := filepath.Join(repo, ".worktrees", "gone")
	live := filepath.Join(repo, ".worktrees", "live")
	if err := os.MkdirAll(filepath.Join(gone, ".tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkLinkedWorktreeDir(t, live)
	vanished := State{SessionState: StateActive, PID: os.Getpid(), RepoPath: repo, WorktreePath: gone, Branch: "gone", SessionKey: "r/gone"}
	if got := vanished.ResolveDisplayState(1); got != StateWorktreeGone {
		t.Errorf("display state = %q, want %q", got, StateWorktreeGone)
	}
	if got := vanished.ResolveState(); got != StateActive {
		t.Errorf("base state = %q, want it unchanged (%q)", got, StateActive)
	}
	healthy := State{SessionState: StateActive, PID: os.Getpid(), RepoPath: repo, WorktreePath: live, Branch: "live", SessionKey: "r/live"}
	if got := healthy.ResolveDisplayState(0); got != StateActive {
		t.Errorf("a worktree with its .git reads %q, want active", got)
	}
	implicit := State{Kind: KindImplicit, SessionState: StateActive, PID: os.Getpid(), RepoPath: gone, WorktreePath: gone, Branch: "main", SessionKey: "r/x"}
	if got := implicit.ResolveDisplayState(0); got == StateWorktreeGone {
		t.Error("an implicit session is never worktree-gone (it has no sc worktree)")
	}
	missing := State{SessionState: StateActive, RepoPath: repo, WorktreePath: filepath.Join(repo, ".worktrees", "absent"), Branch: "absent", SessionKey: "r/absent"}
	if got := missing.ResolveDisplayState(0); got != StateAbandoned {
		t.Errorf("a missing directory stays %q, got %q", StateAbandoned, got)
	}
	rows := ListRows([]State{vanished}, false)
	if len(rows) != 1 || rows[0].State != StateWorktreeGone {
		t.Errorf("sc list rows = %+v, want the session listed as %s", rows, StateWorktreeGone)
	}
}
