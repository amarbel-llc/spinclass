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

	_, err := Choose("/tmp/empty-repo", "resume")
	if err == nil {
		t.Fatal("expected error for empty session list")
	}
	if !strings.Contains(err.Error(), "no sessions for empty-repo") {
		t.Errorf("error = %q, want contains 'no sessions for empty-repo'", err.Error())
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
	_, err := Choose(live, "close")
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
