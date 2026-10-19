package profilerights

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/confinement"
)

func writeMapping(t *testing.T, state, principal, key string) string {
	t.Helper()
	path := confinement.PrincipalMappingPath(state, principal)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"version":1,"principal":"%s","session_key":"%s","worktree":"/wt/%s","unit":"u.service"}`, principal, key, principal)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The session's own mapping goes, found by the mapping's session key; another
// session's mapping stays even if this session's worktree state was rewritten
// to claim its principal.
func TestRevokeSessionRemovesOnlyItsOwnPrincipalMapping(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	own := writeMapping(t, state, "p-1", "repo/child")
	other := writeMapping(t, state, "p-2", "repo/other")

	if err := RevokeSession("repo/child"); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, err := os.Stat(own); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("own mapping survived session end: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another session's mapping was removed: %v", err)
	}
}
