package git

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/store"
)

// stashLocalFixture is confinedFixture with a tracked file committed in the
// main checkout, the session worktree and the foreign worktree, so each can be
// made dirty and shelved independently.
func stashLocalFixture(t *testing.T) fixture {
	t.Helper()
	f := confinedFixture(t)
	for _, dir := range []string{f.repo, f.worktree, f.foreignWorktree} {
		write(t, dir, "f.txt", "base\n")
		runGit(t, dir, "add", "f.txt")
		runGit(t, dir, "commit", "-m", "add f.txt")
	}
	return f
}

// shelfSnapshot lists every file under <worktree>/.spinclass/stash with its
// content, so a refused call can be shown to have left the shelf untouched.
func shelfSnapshot(t *testing.T, worktree string) string {
	t.Helper()
	root := filepath.Join(worktree, ".spinclass", "stash")
	var lines []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			lines = append(lines, rel+"/")
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lines = append(lines, rel+"="+string(raw))
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func refsStash(t *testing.T, repo string) string {
	t.Helper()
	out, _ := gitCmd(repo, "rev-parse", "--verify", "refs/stash").CombinedOutput()
	return string(out)
}

func stashCallers(f fixture) map[string]store.Session {
	noSession := f.session(store.ConfinementUnresolved, store.Identity{})
	noSession.NoSession = true
	return map[string]store.Session{
		"confined":            f.session(store.ConfinementConfined, store.Identity{}),
		"unconfined resolved": f.session(store.ConfinementUnconfined, store.Identity{}),
		"unresolved":          f.session(store.ConfinementUnresolved, store.Identity{}),
		"no session":          noSession,
	}
}

func TestStashIsSessionLocalForEveryCaller(t *testing.T) {
	f := stashLocalFixture(t)

	// The main checkout holds a plain `git stash` entry and its own shelf entry;
	// the foreign worktree holds a shelf entry and is left dirty.
	write(t, f.repo, "f.txt", "main plain\n")
	runGit(t, f.repo, "stash")
	write(t, f.repo, "f.txt", "main shelved\n")
	if _, err := NewShelf().Save(f.repo, store.ShelfSaveRequest{Message: "main shelf"}); err != nil {
		t.Fatal(err)
	}
	write(t, f.foreignWorktree, "f.txt", "foreign shelved\n")
	if _, err := NewShelf().Save(f.foreignWorktree, store.ShelfSaveRequest{Message: "foreign shelf"}); err != nil {
		t.Fatal(err)
	}
	write(t, f.foreignWorktree, "f.txt", "foreign dirty\n")
	write(t, f.repo, "f.txt", "main dirty\n")

	shelfBefore := map[string]string{f.repo: shelfSnapshot(t, f.repo), f.foreignWorktree: shelfSnapshot(t, f.foreignWorktree)}
	statusBefore := map[string]string{f.repo: porcelain(t, f.repo), f.foreignWorktree: porcelain(t, f.foreignWorktree)}
	stashRefBefore := refsStash(t, f.repo)
	if strings.Contains(stashRefBefore, "fatal") {
		t.Fatalf("the plain git stash entry must exist: %q", stashRefBefore)
	}
	if shelfBefore[f.repo] == "" || shelfBefore[f.foreignWorktree] == "" {
		t.Fatal("both foreign shelves must hold an entry")
	}

	targets := map[string]string{
		"foreign worktree":      f.foreignWorktree,
		"main checkout":         f.repo,
		"relative to foreign":   filepath.Join("..", "other-session"),
		"path git cannot parse": filepath.Join(f.worktree, "does", "not", "exist"),
	}
	for callerName, session := range stashCallers(f) {
		for targetName, target := range targets {
			for _, sub := range []string{"list", "apply", "drop", "save"} {
				t.Run(callerName+"/"+targetName+"/"+sub, func(t *testing.T) {
					_, err := doAs(t, session, "stash", sub, map[string]any{"repo_path": target})
					if err == nil {
						t.Fatal("a repo_path outside the session worktree must be refused")
					}
					if callerName != "no session" && !strings.Contains(err.Error(), stashSessionLocalRefusal) {
						t.Fatalf("refusal text = %q, want %q", err, stashSessionLocalRefusal)
					}
				})
			}
		}
	}

	for _, dir := range []string{f.repo, f.foreignWorktree} {
		if got := shelfSnapshot(t, dir); got != shelfBefore[dir] {
			t.Fatalf("shelf of %s changed:\nbefore:\n%s\nafter:\n%s", dir, shelfBefore[dir], got)
		}
		if got := porcelain(t, dir); got != statusBefore[dir] {
			t.Fatalf("working tree of %s changed: %q -> %q", dir, statusBefore[dir], got)
		}
	}
	if got := refsStash(t, f.repo); got != stashRefBefore {
		t.Fatalf("refs/stash changed: %q -> %q", stashRefBefore, got)
	}
	if got := gitOutput(t, f.repo, "stash", "list"); !strings.Contains(got, "stash@{0}") {
		t.Fatalf("the plain git stash entry must survive: %q", got)
	}
}

func TestStashOnTheOwnWorktreeWorksForEveryResolvedCaller(t *testing.T) {
	for _, confinement := range []store.Confinement{store.ConfinementConfined, store.ConfinementUnconfined} {
		for _, repoPath := range []string{"", "own", "."} {
			t.Run(confinementNames[confinement]+"/repo_path="+repoPath, func(t *testing.T) {
				f := stashLocalFixture(t)
				s := f.session(confinement, store.Identity{})
				args := func(extra map[string]any) map[string]any {
					a := map[string]any{}
					switch repoPath {
					case "own":
						a["repo_path"] = f.worktree
					case ".":
						a["repo_path"] = "."
					}
					for k, v := range extra {
						a[k] = v
					}
					return a
				}
				write(t, f.worktree, "f.txt", "session work\n")
				if _, err := doAs(t, s, "stash", "save", args(map[string]any{"message": "mine"})); err != nil {
					t.Fatal(err)
				}
				out, err := doAs(t, s, "stash", "list", args(nil))
				if err != nil || !strings.Contains(out, "mine") {
					t.Fatalf("list = %q, %v", out, err)
				}
				if _, err := doAs(t, s, "stash", "apply", args(nil)); err != nil {
					t.Fatal(err)
				}
				if got := readFile(t, f.worktree, "f.txt"); got != "session work\n" {
					t.Fatalf("apply restored %q", got)
				}
				if _, err := doAs(t, s, "stash", "drop", args(nil)); err != nil {
					t.Fatal(err)
				}
				if got := entryCount(t, f.worktree); got != 0 {
					t.Fatalf("%d entries after drop", got)
				}
			})
		}
	}
}

// A caller with no session worktree has nothing to shelve, with or without a
// repo_path.
func TestStashRefusesWithoutASessionWorktree(t *testing.T) {
	f := stashLocalFixture(t)
	for _, confinement := range []store.Confinement{store.ConfinementUnresolved, store.ConfinementUnconfined, store.ConfinementConfined} {
		s := store.Session{Confinement: confinement}
		for _, sub := range []string{"list", "apply", "drop", "save"} {
			for _, a := range []map[string]any{nil, {"repo_path": f.repo}} {
				if _, err := doAs(t, s, "stash", sub, a); err == nil {
					t.Fatalf("%s/%s %v with no session dir must be refused", confinementNames[confinement], sub, a)
				}
			}
		}
	}
}

// Foreign repo_path is still a prompt (Ask), not an automatic Allow, for the
// hook; the body refusal above is a second, independent line.
func TestStashForeignRepoPathStillClassifiesAsk(t *testing.T) {
	f := newFixture(t)
	for _, sub := range []string{"list", "save", "apply", "drop"} {
		for _, target := range []string{f.foreignWorktree, f.repo} {
			if got := New().Classify(f.own(), op(t, "stash", sub, map[string]any{"repo_path": target})); got != store.Ask {
				t.Fatalf("stash %s repo_path=%s classified %v, want Ask", sub, target, got)
			}
		}
	}
}
