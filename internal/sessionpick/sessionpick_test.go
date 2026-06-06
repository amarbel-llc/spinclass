package sessionpick

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
)

// TestChooseEmptyListErrors covers the no-sessions-for-this-repo path:
// even on a TTY, Choose returns a clean error rather than rendering an
// empty huh menu.
func TestChooseEmptyListErrors(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	_, err := Choose("/tmp/empty-repo", "resume", nil)
	if err == nil {
		t.Fatal("expected error for empty session list")
	}
	if !strings.Contains(err.Error(), "no sessions for empty-repo") {
		t.Errorf("error = %q, want contains 'no sessions for empty-repo'", err.Error())
	}
}

// writeSessions persists one inactive-but-listed session per branch
// under a fresh XDG_STATE_HOME, with live worktree dirs so
// ResolveState() doesn't mark them abandoned. Returns the repo path.
func writeSessions(t *testing.T, branches ...string) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	live := t.TempDir()
	for _, branch := range branches {
		wt := filepath.Join(live, ".worktrees", branch)
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		s := session.State{
			PID:          12345, // not alive — flips to inactive, but still listed
			SessionState: session.StateActive,
			RepoPath:     live,
			WorktreePath: wt,
			Branch:       branch,
			SessionKey:   "repo/" + branch,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		}
		if err := session.Write(s); err != nil {
			t.Fatal(err)
		}
	}
	return live
}

// TestChooseAutoSingleReturnsLoneSession: with exactly one candidate,
// ChooseAutoSingle returns it without rendering the picker (auto=true)
// — even on a non-TTY stdin, since the caller owns any confirmation.
func TestChooseAutoSingleReturnsLoneSession(t *testing.T) {
	live := writeSessions(t, "feature")

	item, auto, err := ChooseAutoSingle(live, "resume", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !auto {
		t.Error("auto = false, want true for a single candidate")
	}
	if item == nil || item.State == nil || item.State.Branch != "feature" {
		t.Fatalf("item = %+v, want the lone 'feature' session", item)
	}
}

// TestChooseAutoSingleMultiNonInteractiveListsIDs: with multiple
// candidates ChooseAutoSingle behaves exactly like Choose — non-TTY
// callers get the ID list error, not an auto-pick.
func TestChooseAutoSingleMultiNonInteractiveListsIDs(t *testing.T) {
	live := writeSessions(t, "feature", "other")

	_, auto, err := ChooseAutoSingle(live, "resume", nil, nil)
	if err == nil {
		t.Fatal("expected non-interactive error for multiple candidates")
	}
	if auto {
		t.Error("auto = true, want false for multiple candidates")
	}
	got := err.Error()
	for _, want := range []string{"feature", "other", "Use: spinclass resume <id>"} {
		if !strings.Contains(got, want) {
			t.Errorf("error = %q, missing %q", got, want)
		}
	}
}

// TestChooseAutoSingleShortcutCountsLocalOnly: the single-candidate
// shortcut counts LOCAL sessions only — cached remote rows alongside a
// lone local session do not suppress it (remote rows are supplementary,
// never the auto-single candidate).
func TestChooseAutoSingleShortcutCountsLocalOnly(t *testing.T) {
	live := writeSessions(t, "feature")
	remoteRows := []Item{
		{TitleText: "remote thing", Detail: "remote(devbox) · active · cached", Filter: "devbox:remote-thing", Target: "devbox:remote-thing"},
	}

	item, auto, err := ChooseAutoSingle(live, "resume", nil, remoteRows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !auto {
		t.Error("auto = false, want true: remote rows must not suppress the lone-local shortcut")
	}
	if item == nil || item.State == nil || item.State.Branch != "feature" {
		t.Fatalf("item = %+v, want the lone local 'feature' session", item)
	}
}

// TestChooseAutoSingleRemoteOnlyShowsPicker: when the ONLY candidates
// are remote rows, the picker still shows — no auto-single (a remote
// row is never the auto-single candidate) and no "no sessions" error.
// On a non-TTY stdin that surfaces as the ID-list error including the
// remote target, proving the picker path was taken.
func TestChooseAutoSingleRemoteOnlyShowsPicker(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	remoteRows := []Item{
		{TitleText: "remote thing", Detail: "remote(devbox) · active · cached", Filter: "devbox:remote-thing", Target: "devbox:remote-thing"},
	}

	_, auto, err := ChooseAutoSingle("/tmp/empty-repo", "resume", nil, remoteRows)
	if err == nil {
		t.Fatal("expected non-interactive picker error for remote-only candidates")
	}
	if auto {
		t.Error("auto = true, want false: a remote row is never the auto-single candidate")
	}
	got := err.Error()
	if strings.Contains(got, "no sessions for") {
		t.Errorf("error = %q: remote-only candidates must reach the picker, not the no-sessions error", got)
	}
	for _, want := range []string{"devbox:remote-thing", "Use: spinclass resume <id>"} {
		if !strings.Contains(got, want) {
			t.Errorf("error = %q, missing %q", got, want)
		}
	}
}

// TestChooseNonInteractiveListsIDs covers the CI / piped-stdin path:
// non-TTY callers get a list of available IDs and a "Use:" hint with
// the supplied command name (resume vs close).
func TestChooseNonInteractiveListsIDs(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)

	// Live worktree dirs so ResolveState() doesn't mark them abandoned.
	live := t.TempDir()
	feat := filepath.Join(live, ".worktrees", "feature")
	other := filepath.Join(live, ".worktrees", "other")
	for _, p := range []string{feat, other} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, branch := range []string{"feature", "other"} {
		s := session.State{
			PID:          12345, // not alive — flips to inactive, but still listed
			SessionState: session.StateActive,
			RepoPath:     live,
			WorktreePath: filepath.Join(live, ".worktrees", branch),
			Branch:       branch,
			SessionKey:   "repo/" + branch,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		}
		if err := session.Write(s); err != nil {
			t.Fatal(err)
		}
	}

	// Stdin in `go test` is /dev/null (non-tty), so interactive() returns
	// false naturally without us redirecting anything.
	_, err := Choose(live, "close", nil)
	if err == nil {
		t.Fatal("expected non-interactive error")
	}
	got := err.Error()
	for _, want := range []string{"feature", "other", "Use: spinclass close <id>"} {
		if !strings.Contains(got, want) {
			t.Errorf("error = %q, missing %q", got, want)
		}
	}
}
