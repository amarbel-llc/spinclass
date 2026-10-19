package session

import (
	"os"
	"path/filepath"
	"testing"
)

// addSecondSession writes another session under the same repo and index as s.
func addSecondSession(t *testing.T, s State, branch, principal string) State {
	t.Helper()
	wt := filepath.Join(s.RepoPath, ".worktrees", branch)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	other := s
	other.WorktreePath = wt
	other.Branch = branch
	other.SessionKey = filepath.Base(s.RepoPath) + "/" + branch
	other.Principal = principal
	if err := Write(other); err != nil {
		t.Fatal(err)
	}
	return other
}

func TestSessionKeyOfPrincipal(t *testing.T) {
	a := setupTestSession(t, "alpha")
	a.Principal = "p-alpha"
	if err := Write(a); err != nil {
		t.Fatal(err)
	}
	b := addSecondSession(t, a, "beta", "p-beta")

	if key, ok := SessionKeyOfPrincipal("p-beta"); !ok || key != b.SessionKey {
		t.Errorf("SessionKeyOfPrincipal(p-beta) = %q, %v; want %q, true", key, ok, b.SessionKey)
	}
	if key, ok := SessionKeyOfPrincipal("p-alpha"); !ok || key != a.SessionKey {
		t.Errorf("SessionKeyOfPrincipal(p-alpha) = %q, %v; want %q, true", key, ok, a.SessionKey)
	}
	if _, ok := SessionKeyOfPrincipal("nobody"); ok {
		t.Error("unknown principal must not resolve")
	}
	if _, ok := SessionKeyOfPrincipal(""); ok {
		t.Error("empty principal must not resolve")
	}
}

func TestSessionKeyOfPrincipalAmbiguousFailsClosed(t *testing.T) {
	a := setupTestSession(t, "alpha")
	a.Principal = "dup"
	if err := Write(a); err != nil {
		t.Fatal(err)
	}
	addSecondSession(t, a, "beta", "dup")

	if key, ok := SessionKeyOfPrincipal("dup"); ok {
		t.Errorf("two claimants must fail closed, got %q", key)
	}
}

func TestSessionKeyOfPrincipalIgnoresTombstones(t *testing.T) {
	a := setupTestSession(t, "alpha")
	a.Principal = "gone"
	if err := Write(a); err != nil {
		t.Fatal(err)
	}
	if err := Tombstone(a.RepoPath, a.Branch, "abc123"); err != nil {
		t.Fatal(err)
	}

	if key, ok := SessionKeyOfPrincipal("gone"); ok {
		t.Errorf("a closed session's principal must not resolve, got %q", key)
	}
}
