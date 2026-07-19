package sysprompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
)

// writeSessionState fabricates an on-disk worktree session for repo/branch
// (state JSON + central index entry under the test's XDG_STATE_HOME) and
// returns the worktree path. pid == os.Getpid() makes it resolve active.
func writeSessionState(t *testing.T, repo, branch string, pid int, desc string) string {
	t.Helper()
	wt := filepath.Join(repo, ".worktrees", branch)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	s := session.State{
		PID:          pid,
		SessionState: session.StateActive,
		RepoPath:     repo,
		WorktreePath: wt,
		Branch:       branch,
		SessionKey:   filepath.Base(repo) + "/" + branch,
		Description:  desc,
		StartedAt:    time.Now(),
	}
	if err := session.Write(s); err != nil {
		t.Fatal(err)
	}
	return wt
}

// Worktree mode: the line lists the other active sessions (with descriptions),
// excludes the current session, skips dead-PID sessions, and pluralizes.
func TestLoadCoActiveLineWorktreeMode(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	repo := filepath.Join(base, "myrepo")

	self := writeSessionState(t, repo, "self", os.Getpid(), "current work")
	writeSessionState(t, repo, "bright-cherry", os.Getpid(), "fix foo")
	writeSessionState(t, repo, "gone-dead", 2147483646, "abandoned ship")

	line := loadCoActiveLine(ModeWorktree, self)
	want := "1 other live session on myrepo: bright-cherry (fix foo)"
	if line != want {
		t.Fatalf("line = %q, want %q", line, want)
	}

	// The rendered fragment carries the line.
	got, err := Render(Coordinates{Mode: ModeWorktree, SessionKey: "k", CoActiveSessions: line})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	mustContain(t, got, "- Co-active: "+want)

	// A second live session pluralizes and stays branch-sorted.
	writeSessionState(t, repo, "zz-olive", os.Getpid(), "")
	line = loadCoActiveLine(ModeWorktree, self)
	want = "2 other live sessions on myrepo: bright-cherry (fix foo), zz-olive"
	if line != want {
		t.Fatalf("plural line = %q, want %q", line, want)
	}
}

// No co-active sessions, and degraded lookups (unreadable current state),
// both yield "" — the line is omitted, never an error.
func TestLoadCoActiveLineOmittedWhenNoneOrUnreadable(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	repo := filepath.Join(base, "myrepo")

	self := writeSessionState(t, repo, "self", os.Getpid(), "")
	if line := loadCoActiveLine(ModeWorktree, self); line != "" {
		t.Errorf("expected no line with no co-active sessions, got %q", line)
	}

	// A corrupt sibling state file contributes nothing (skipped by the index
	// walk), so the line stays omitted rather than erroring.
	corruptWt := writeSessionState(t, repo, "corrupt", os.Getpid(), "")
	if err := os.WriteFile(filepath.Join(corruptWt, ".spinclass", "state.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if line := loadCoActiveLine(ModeWorktree, self); line != "" {
		t.Errorf("expected no line with only a corrupt sibling, got %q", line)
	}

	// An unreadable CURRENT state degrades to omission even when another
	// active session exists (the repo anchor cannot be resolved).
	writeSessionState(t, repo, "bright-cherry", os.Getpid(), "")
	if err := os.WriteFile(filepath.Join(self, ".spinclass", "state.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if line := loadCoActiveLine(ModeWorktree, self); line != "" {
		t.Errorf("expected no line when the current session state is unreadable, got %q", line)
	}

	// An empty path (nothing resolvable) is a clean omission too.
	if line := loadCoActiveLine(ModeWorktree, ""); line != "" {
		t.Errorf("expected no line for an empty path, got %q", line)
	}
}

// Main-checkout mode: worktree sessions on the repo are listed; implicit
// sessions at the checkout are excluded wholesale (the current implicit
// session is indistinguishable from siblings sharing the checkout path).
func TestLoadCoActiveLineMainCheckout(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	checkout := filepath.Join(base, "myrepo")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	writeSessionState(t, checkout, "bright-olive", os.Getpid(), "")
	imp := session.State{
		Kind:         session.KindImplicit,
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		RepoPath:     checkout,
		WorktreePath: checkout,
		Branch:       "master",
		StartedAt:    time.Now(),
	}
	if err := session.WriteImplicit(imp, "rand1234"); err != nil {
		t.Fatal(err)
	}

	line := loadCoActiveLine(ModeMainCheckout, checkout)
	want := "1 other live session on myrepo: bright-olive"
	if line != want {
		t.Fatalf("line = %q, want %q", line, want)
	}
}

// The template omits the co-active bullet when CoActiveSessions is empty, in
// both variants.
func TestRenderCoActiveLineOmittedWhenEmpty(t *testing.T) {
	for _, mode := range []Mode{ModeWorktree, ModeMainCheckout} {
		got, err := Render(Coordinates{Mode: mode, SessionKey: "k"})
		if err != nil {
			t.Fatalf("Render(%s): %v", mode, err)
		}
		if strings.Contains(got, "Co-active") {
			t.Errorf("%s: co-active line must be omitted when empty:\n%s", mode, got)
		}

		line := "2 other live sessions on myrepo: a, b"
		got, err = Render(Coordinates{Mode: mode, SessionKey: "k", CoActiveSessions: line})
		if err != nil {
			t.Fatalf("Render(%s): %v", mode, err)
		}
		mustContain(t, got, "- Co-active: "+line)
	}
}
