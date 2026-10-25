package nixgc

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSweatfileFor(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sweatfile"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Disabled is the cascade value (global, ancestors, main checkout), overridden
// by the worktree's own disable-nix-gc when it sets one; a corrupt worktree
// file is no override.
func TestDisabledCascadeAndWorktreeOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "work", "repo")
	wt := filepath.Join(repo, ".worktrees", "x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}

	if Disabled(repo, wt) {
		t.Error("no sweatfile anywhere: want enabled")
	}

	writeSweatfileFor(t, wt, "[hooks]\ndisable-nix-gc = true\n")
	if !Disabled(repo, wt) {
		t.Error("worktree disable-nix-gc = true not honoured")
	}

	writeSweatfileFor(t, repo, "[hooks]\ndisable-nix-gc = true\n")
	writeSweatfileFor(t, wt, "[hooks]\ndisable-nix-gc = false\n")
	if Disabled(repo, wt) {
		t.Error("worktree disable-nix-gc = false should override the repo's true")
	}

	writeSweatfileFor(t, wt, "[hooks\n=== not toml\n")
	if !Disabled(repo, wt) {
		t.Error("a corrupt worktree sweatfile must leave the repo's value in force")
	}

	if err := os.Remove(filepath.Join(wt, "sweatfile")); err != nil {
		t.Fatal(err)
	}
	if !Disabled(repo, wt) {
		t.Error("no worktree sweatfile: the repo's value must apply")
	}
}
