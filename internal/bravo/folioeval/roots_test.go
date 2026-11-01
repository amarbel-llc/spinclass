package folioeval

import (
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// Resolve follows symlinks on the parent and keeps the leaf as named, so a
// link inside the worktree stays a worktree path while a worktree behind a
// linked parent resolves to its real place; a missing tail is re-appended.
func TestResolveParentNotLeaf(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(real, "result")); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		filepath.Join(link, "sub", "f"):       filepath.Join(real, "sub", "f"),
		filepath.Join(real, "result"):         filepath.Join(real, "result"),
		filepath.Join(link, "result"):         filepath.Join(real, "result"),
		filepath.Join(link, "missing", "x"):   filepath.Join(real, "missing", "x"),
		filepath.Join(real, "sub", "..", "f"): filepath.Join(real, "f"),
		"relative/x":                          "relative/x",
		"/":                                   "/",
	} {
		if got := Resolve(in); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", in, got, want)
		}
	}
}

// Children are the worktrees of live, non-implicit sessions the principal
// holds an accepted handle on; a pending grant, a tombstone, an implicit
// session or an empty principal yields none.
func TestRootsForChildrenAreHeldSessions(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "repo")
	me := filepath.Join(repo, ".worktrees", "me")
	kid := filepath.Join(repo, ".worktrees", "kid")
	for _, d := range []string{me, kid} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	states := []session.State{
		{WorktreePath: kid, Holders: []string{"p1"}},
		{WorktreePath: filepath.Join(repo, ".worktrees", "pending"), PendingHandles: []string{"p1"}},
		{WorktreePath: filepath.Join(repo, ".worktrees", "theirs"), Holders: []string{"p2"}},
		{WorktreePath: repo, Kind: session.KindImplicit, Holders: []string{"p1"}},
	}
	r := RootsFor(me, repo, filepath.Join(dir, "home"), "p1", os.Getenv, states)
	if len(r.Children) != 1 || r.Children[0] != kid {
		t.Errorf("Children = %v, want [%s]", r.Children, kid)
	}
	if r.Pwd != me || r.Worktree != me || r.Repo != repo || r.Worktrees != filepath.Join(repo, ".worktrees") || r.SiblingsParent != dir || r.SiblingsExclude != repo || r.NixStore != "/nix/store" {
		t.Errorf("roots = %+v", r)
	}
	if got := RootsFor(me, repo, "", "", os.Getenv, states); len(got.Children) != 0 {
		t.Errorf("an empty principal must hold nothing, got %v", got.Children)
	}
	if got := RootsFor(me, "", "", "p1", nil, nil); got.Repo != "" || got.Worktrees != "" || got.SiblingsParent != "" || got.Env("HOME") != "" {
		t.Errorf("no repo root: spinclass roots and env must match nothing, got %+v", got)
	}
}
