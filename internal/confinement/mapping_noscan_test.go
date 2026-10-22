package confinement

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
)

// A principal resolves to a session through the frontend mapping and nowhere
// else. A session record that merely CLAIMS the principal (a confined worker can
// write its own worktree copy to say anything) must not resolve, and the error
// names the principal and the missing mapping path.
func TestSessionKeyOfPrincipalWithoutAMappingFailsClosed(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	repo := filepath.Join(t.TempDir(), "repo")
	wt := filepath.Join(repo, ".worktrees", "claimant")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := session.Write(session.State{
		SessionState: session.StateActive,
		RepoPath:     repo,
		WorktreePath: wt,
		Branch:       "claimant",
		SessionKey:   "repo/claimant",
		Principal:    "claimed-principal",
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	key, err := SessionKeyOfPrincipal("claimed-principal")
	if err == nil {
		t.Fatalf("a recorded principal with no frontend mapping resolved to %q", key)
	}
	for _, want := range []string{"claimed-principal", PrincipalMappingPath(state, "claimed-principal")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// An unconfined session resolves the same way once the SessionStart hook has
// written its mapping into frontend state.
func TestSessionKeyOfPrincipalResolvesAnUnconfinedMapping(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	repo := filepath.Join(t.TempDir(), "repo")
	wt := filepath.Join(repo, ".worktrees", "plain")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := session.Write(session.State{
		SessionState: session.StateActive, RepoPath: repo, WorktreePath: wt,
		Branch: "plain", SessionKey: "repo/plain", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := RecordUnconfinedPrincipal(state, "plain-principal", "repo/plain", wt); err != nil {
		t.Fatal(err)
	}
	if key, err := SessionKeyOfPrincipal("plain-principal"); err != nil || key != "repo/plain" {
		t.Errorf("SessionKeyOfPrincipal = %q, %v; want repo/plain", key, err)
	}
}
