package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/store"
)

// newShelfRepo is newRepo with .spinclass/ excluded the way a session worktree
// excludes it, so status output shows only real work.
func newShelfRepo(t *testing.T) string {
	t.Helper()
	repo := newRepo(t)
	excludeShelfDir(t, repo)
	return repo
}

func excludeShelfDir(t *testing.T, repo string) {
	t.Helper()
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, []byte(".spinclass/\n.worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd
}

func porcelain(t *testing.T, dir string) string {
	t.Helper()
	out, err := gitCmd(dir, "status", "--porcelain", "--untracked-files=all").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	return strings.TrimRight(string(out), "\n")
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func mustDo(t *testing.T, repo, sub string, args map[string]any) string {
	t.Helper()
	out, err := do(t, repo, "stash", sub, args)
	if err != nil {
		t.Fatalf("stash %s %v: %v", sub, args, err)
	}
	return out
}

func entryCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := NewShelf().List(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestShelfRoundtrips(t *testing.T) {
	cases := []struct {
		name            string
		setup           func(t *testing.T, repo string)
		saveArgs        map[string]any
		statusAfterSave string
		statusAfterPut  string
		check           func(t *testing.T, repo string)
	}{
		{
			name:            "tracked edit",
			setup:           func(t *testing.T, repo string) { write(t, repo, "a.txt", "two\n") },
			statusAfterSave: "",
			statusAfterPut:  " M a.txt",
			check: func(t *testing.T, repo string) {
				if got := readFile(t, repo, "a.txt"); got != "two\n" {
					t.Fatalf("a.txt = %q", got)
				}
			},
		},
		{
			name: "untracked files are left alone without include_untracked",
			setup: func(t *testing.T, repo string) {
				write(t, repo, "a.txt", "two\n")
				write(t, repo, "loose.txt", "x\n")
			},
			statusAfterSave: "?? loose.txt",
			statusAfterPut:  " M a.txt\n?? loose.txt",
		},
		{
			name: "include_untracked takes nested files and removes emptied directories",
			setup: func(t *testing.T, repo string) {
				write(t, repo, "a.txt", "two\n")
				if err := os.MkdirAll(filepath.Join(repo, "deep", "er"), 0o755); err != nil {
					t.Fatal(err)
				}
				write(t, repo, "deep/er/n.txt", "n\n")
				write(t, repo, "top.txt", "t\n")
			},
			saveArgs:        map[string]any{"include_untracked": true},
			statusAfterSave: "",
			statusAfterPut:  " M a.txt\n?? deep/er/n.txt\n?? top.txt",
			check: func(t *testing.T, repo string) {
				if got := readFile(t, repo, "deep/er/n.txt"); got != "n\n" {
					t.Fatalf("n.txt = %q", got)
				}
			},
		},
		{
			name: "untracked only",
			setup: func(t *testing.T, repo string) {
				write(t, repo, "only.txt", "o\n")
			},
			saveArgs:        map[string]any{"include_untracked": true},
			statusAfterSave: "",
			statusAfterPut:  "?? only.txt",
		},
		{
			name: "untracked executable keeps its mode",
			setup: func(t *testing.T, repo string) {
				write(t, repo, "run.sh", "#!/bin/sh\n")
				if err := os.Chmod(filepath.Join(repo, "run.sh"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			saveArgs:        map[string]any{"include_untracked": true},
			statusAfterSave: "",
			statusAfterPut:  "?? run.sh",
			check: func(t *testing.T, repo string) {
				info, err := os.Stat(filepath.Join(repo, "run.sh"))
				if err != nil || info.Mode().Perm() != 0o755 {
					t.Fatalf("run.sh mode = %v, %v", info, err)
				}
			},
		},
		{
			name: "untracked symlink",
			setup: func(t *testing.T, repo string) {
				if err := os.Symlink("a.txt", filepath.Join(repo, "link")); err != nil {
					t.Fatal(err)
				}
			},
			saveArgs:        map[string]any{"include_untracked": true},
			statusAfterSave: "",
			statusAfterPut:  "?? link",
			check: func(t *testing.T, repo string) {
				if target, err := os.Readlink(filepath.Join(repo, "link")); err != nil || target != "a.txt" {
					t.Fatalf("link -> %q, %v", target, err)
				}
			},
		},
		{
			name: "index state survives",
			setup: func(t *testing.T, repo string) {
				write(t, repo, "staged.txt", "s\n")
				mustRun(t, repo, "add", "staged.txt")
				write(t, repo, "a.txt", "two\n")
			},
			statusAfterSave: "",
			statusAfterPut:  " M a.txt\nA  staged.txt",
			check: func(t *testing.T, repo string) {
				entryDir := filepath.Join(repo, ".spinclass", "stash", "1")
				if _, err := os.Stat(filepath.Join(entryDir, "index.patch")); err != nil {
					t.Fatalf("index.patch must exist when the index had changes: %v", err)
				}
			},
		},
		{
			name: "partially staged file keeps both layers",
			setup: func(t *testing.T, repo string) {
				write(t, repo, "a.txt", "two\n")
				mustRun(t, repo, "add", "a.txt")
				write(t, repo, "a.txt", "three\n")
			},
			statusAfterSave: "",
			statusAfterPut:  "MM a.txt",
			check: func(t *testing.T, repo string) {
				if got := gitOutput(t, repo, "show", ":a.txt"); got != "two" {
					t.Fatalf("index a.txt = %q", got)
				}
				if got := readFile(t, repo, "a.txt"); got != "three\n" {
					t.Fatalf("worktree a.txt = %q", got)
				}
			},
		},
		{
			name: "paths filter shelves only the named paths",
			setup: func(t *testing.T, repo string) {
				commitFile(t, repo, "c.txt", "c\n", "add c")
				write(t, repo, "a.txt", "two\n")
				write(t, repo, "c.txt", "cc\n")
				write(t, repo, "u1.txt", "1\n")
				write(t, repo, "u2.txt", "2\n")
			},
			saveArgs:        map[string]any{"paths": []string{"a.txt", "u1.txt"}, "include_untracked": true},
			statusAfterSave: " M c.txt\n?? u2.txt",
			statusAfterPut:  " M a.txt\n M c.txt\n?? u1.txt\n?? u2.txt",
		},
		{
			name: "deleted file",
			setup: func(t *testing.T, repo string) {
				if err := os.Remove(filepath.Join(repo, "a.txt")); err != nil {
					t.Fatal(err)
				}
			},
			statusAfterSave: "",
			statusAfterPut:  " D a.txt",
		},
		{
			name: "binary content",
			setup: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, "bin.dat"), []byte{0, 1, 2, 255, 0}, 0o644); err != nil {
					t.Fatal(err)
				}
				mustRun(t, repo, "add", "bin.dat")
				mustRun(t, repo, "commit", "-m", "bin")
				if err := os.WriteFile(filepath.Join(repo, "bin.dat"), []byte{9, 0, 8, 255}, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			statusAfterSave: "",
			statusAfterPut:  " M bin.dat",
			check: func(t *testing.T, repo string) {
				if got := readFile(t, repo, "bin.dat"); got != string([]byte{9, 0, 8, 255}) {
					t.Fatalf("bin.dat = %v", []byte(got))
				}
			},
		},
		{
			name: "mode change",
			setup: func(t *testing.T, repo string) {
				if err := os.Chmod(filepath.Join(repo, "a.txt"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			statusAfterSave: "",
			statusAfterPut:  " M a.txt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newShelfRepo(t)
			tc.setup(t, repo)

			mustDo(t, repo, "save", tc.saveArgs)
			if got := porcelain(t, repo); got != tc.statusAfterSave {
				t.Fatalf("status after save = %q, want %q", got, tc.statusAfterSave)
			}
			if got := gitOutput(t, repo, "stash", "list"); got != "" {
				t.Fatalf("git's own stash must stay empty, got %q", got)
			}

			mustDo(t, repo, "apply", nil)
			if got := porcelain(t, repo); got != tc.statusAfterPut {
				t.Fatalf("status after apply = %q, want %q", got, tc.statusAfterPut)
			}
			if entryCount(t, repo) != 1 {
				t.Fatal("apply must keep the entry")
			}
			if tc.check != nil {
				tc.check(t, repo)
			}

			mustDo(t, repo, "drop", nil)
			if entryCount(t, repo) != 0 {
				t.Fatal("drop must remove the entry")
			}
			if _, err := os.Stat(filepath.Join(repo, ".spinclass", "stash", "1")); !os.IsNotExist(err) {
				t.Fatalf("entry directory must be gone: %v", err)
			}
		})
	}
}

func TestShelfNothingToSave(t *testing.T) {
	repo := newShelfRepo(t)
	if _, err := do(t, repo, "stash", "save", nil); !errors.Is(err, errNothingToStash) {
		t.Fatalf("clean tree: %v", err)
	}
	write(t, repo, "loose.txt", "x\n")
	if _, err := do(t, repo, "stash", "save", nil); !errors.Is(err, errNothingToStash) {
		t.Fatalf("untracked only, without include_untracked: %v", err)
	}
	if entryCount(t, repo) != 0 {
		t.Fatal("a refused save must leave no entry")
	}
}

func TestShelfSaveMessages(t *testing.T) {
	repo := newShelfRepo(t)
	write(t, repo, "a.txt", "two\n")
	mustDo(t, repo, "save", map[string]any{"message": "half done"})
	write(t, repo, "a.txt", "three\n")
	mustDo(t, repo, "save", nil)

	entries, err := NewShelf().List(repo)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries: %v, %v", entries, err)
	}
	if entries[0].Message != "WIP on main: "+gitOutput(t, repo, "log", "-1", "--format=%h %s") {
		t.Errorf("default message: %q", entries[0].Message)
	}
	if entries[1].Message != "On main: half done" || entries[1].Branch != "main" {
		t.Errorf("explicit message: %+v", entries[1])
	}
	if entries[1].BaseSHA != gitOutput(t, repo, "rev-parse", "HEAD") {
		t.Errorf("base sha: %+v", entries[1])
	}
}

func TestShelfNumberingAndRefs(t *testing.T) {
	repo := newShelfRepo(t)
	save := func(content string) {
		write(t, repo, "a.txt", content)
		mustDo(t, repo, "save", map[string]any{"message": strings.TrimSpace(content)})
	}
	ids := func() []int {
		entries, err := NewShelf().List(repo)
		if err != nil {
			t.Fatal(err)
		}
		var got []int
		for _, entry := range entries {
			got = append(got, entry.ID)
		}
		return got
	}
	wantIDs := func(want ...int) {
		t.Helper()
		got := ids()
		if len(got) != len(want) {
			t.Fatalf("ids = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("ids = %v, want %v", got, want)
			}
		}
	}

	save("v1\n")
	save("v2\n")
	save("v3\n")
	wantIDs(3, 2, 1)

	mustDo(t, repo, "drop", map[string]any{"stash_ref": "stash@{1}"})
	wantIDs(3, 1)

	save("v4\n")
	wantIDs(4, 3, 1)

	mustDo(t, repo, "apply", map[string]any{"stash_ref": "1"})
	if got := readFile(t, repo, "a.txt"); got != "v1\n" {
		t.Fatalf("apply by entry number: %q", got)
	}
	mustRun(t, repo, "checkout", "--", "a.txt")
	mustDo(t, repo, "apply", map[string]any{"stash_ref": "stash@{1}"})
	if got := readFile(t, repo, "a.txt"); got != "v3\n" {
		t.Fatalf("apply by position: %q", got)
	}
	mustRun(t, repo, "checkout", "--", "a.txt")

	list := mustDo(t, repo, "list", nil)
	if !strings.HasPrefix(list, "stash@{0} (entry 4): On main: v4") || !strings.Contains(list, "stash@{2} (entry 1)") {
		t.Fatalf("list output:\n%s", list)
	}

	mustDo(t, repo, "drop", nil)
	wantIDs(3, 1)

	for _, ref := range []string{"stash@{2}", "7", "stash", "-1", "stash@{x}"} {
		if _, err := do(t, repo, "stash", "drop", map[string]any{"stash_ref": ref}); err == nil {
			t.Errorf("drop %q must fail", ref)
		}
	}
	wantIDs(3, 1)
}

func TestShelfEmptyShelfErrors(t *testing.T) {
	repo := newShelfRepo(t)
	for _, sub := range []string{"apply", "drop"} {
		if _, err := do(t, repo, "stash", sub, nil); err == nil || !strings.Contains(err.Error(), "no stash entries") {
			t.Errorf("%s on an empty shelf: %v", sub, err)
		}
	}
	if out := mustDo(t, repo, "list", nil); out != "no stash entries" {
		t.Errorf("list: %q", out)
	}
}

func TestShelfApplyConflictKeepsEntry(t *testing.T) {
	repo := newShelfRepo(t)
	write(t, repo, "a.txt", "mine\n")
	write(t, repo, "extra.txt", "e\n")
	mustDo(t, repo, "save", map[string]any{"include_untracked": true})

	commitFile(t, repo, "a.txt", "theirs\n", "move the base")

	_, err := do(t, repo, "stash", "apply", nil)
	var conflict *store.ShelfConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want *ShelfConflictError, got %v", err)
	}
	if len(conflict.Paths) != 1 || conflict.Paths[0] != "a.txt" || conflict.Entry.ID != 1 {
		t.Fatalf("conflict: %+v", conflict)
	}
	for _, want := range []string{"conflicted paths: a.txt", "was kept", "stash drop"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if got := readFile(t, repo, "a.txt"); !strings.Contains(got, "<<<<<<<") || !strings.Contains(got, "mine") || !strings.Contains(got, "theirs") {
		t.Fatalf("a.txt must carry conflict markers, got %q", got)
	}
	if got := readFile(t, repo, "extra.txt"); got != "e\n" {
		t.Fatalf("untracked files are still restored on a conflict, got %q", got)
	}
	if entryCount(t, repo) != 1 {
		t.Fatal("a conflicting apply must keep the entry")
	}
}

func TestShelfApplyRefusesBeforeTouchingAnything(t *testing.T) {
	repo := newShelfRepo(t)
	write(t, repo, "a.txt", "two\n")
	write(t, repo, "n.txt", "from shelf\n")
	mustDo(t, repo, "save", map[string]any{"include_untracked": true})
	write(t, repo, "n.txt", "in the way\n")

	_, err := do(t, repo, "stash", "apply", nil)
	wantErrContaining(t, err, "already exist: n.txt")
	if got := readFile(t, repo, "a.txt"); got != "one\n" {
		t.Fatalf("a refused apply must not touch tracked files, got %q", got)
	}
	if got := readFile(t, repo, "n.txt"); got != "in the way\n" {
		t.Fatalf("n.txt = %q", got)
	}

	if err := os.Remove(filepath.Join(repo, "n.txt")); err != nil {
		t.Fatal(err)
	}
	mustDo(t, repo, "apply", nil)
	if got := readFile(t, repo, "n.txt"); got != "from shelf\n" {
		t.Fatalf("n.txt after retry = %q", got)
	}
}

func TestShelfApplyOntoOtherLocalChanges(t *testing.T) {
	repo := newShelfRepo(t)
	commitFile(t, repo, "c.txt", "c\n", "add c")
	write(t, repo, "a.txt", "two\n")
	mustDo(t, repo, "save", nil)
	write(t, repo, "c.txt", "cc\n")

	mustDo(t, repo, "apply", nil)
	if got := porcelain(t, repo); got != " M a.txt\n M c.txt" {
		t.Fatalf("status = %q", got)
	}
}

// An unstaged edit to a file the entry also touches is refused by git apply
// (the 3-way merge needs the file to match the index); nothing may change and
// the entry must survive so the apply can be retried after committing or
// stashing the edit.
func TestShelfApplyOntoSameFileEditIsRefusedIntact(t *testing.T) {
	repo := newShelfRepo(t)
	write(t, repo, "a.txt", "two\n")
	mustDo(t, repo, "save", nil)
	write(t, repo, "a.txt", "local edit\n")

	_, err := do(t, repo, "stash", "apply", nil)
	if err == nil {
		t.Fatal("applying over an unstaged edit of the same file must fail")
	}
	var conflict *store.ShelfConflictError
	if errors.As(err, &conflict) {
		t.Fatalf("a refusal is not a conflict: %v", err)
	}
	if got := readFile(t, repo, "a.txt"); got != "local edit\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if entryCount(t, repo) != 1 {
		t.Fatal("the entry must survive a refused apply")
	}
}

func TestShelfSaveRefusesUnresolvedConflicts(t *testing.T) {
	repo := newShelfRepo(t)
	mustRun(t, repo, "checkout", "-b", "side")
	commitFile(t, repo, "a.txt", "side\n", "side")
	mustRun(t, repo, "checkout", "main")
	commitFile(t, repo, "a.txt", "main\n", "main")
	cmd := gitCmd(repo, "merge", "side")
	_ = cmd.Run()

	_, err := do(t, repo, "stash", "save", nil)
	wantErrContaining(t, err, "unresolved conflicts: a.txt")
}

func TestShelfIsolatedBetweenWorktrees(t *testing.T) {
	repo := newShelfRepo(t)
	worktreeA := filepath.Join(repo, ".worktrees", "alpha-one")
	worktreeB := filepath.Join(repo, ".worktrees", "beta-two")
	mustRun(t, repo, "worktree", "add", "-b", "alpha-one", worktreeA)
	mustRun(t, repo, "worktree", "add", "-b", "beta-two", worktreeB)

	write(t, worktreeA, "a.txt", "from alpha\n")
	mustDo(t, worktreeA, "save", map[string]any{"message": "alpha work"})
	write(t, worktreeB, "a.txt", "from beta\n")
	write(t, worktreeB, "b-only.txt", "b\n")
	mustDo(t, worktreeB, "save", map[string]any{"message": "beta work", "include_untracked": true})
	write(t, worktreeB, "a.txt", "beta again\n")
	mustDo(t, worktreeB, "save", map[string]any{"message": "beta second"})

	listA := mustDo(t, worktreeA, "list", nil)
	listB := mustDo(t, worktreeB, "list", nil)
	if !strings.Contains(listA, "alpha work") || strings.Contains(listA, "beta") || strings.Count(listA, "\n") != 0 {
		t.Fatalf("alpha's shelf:\n%s", listA)
	}
	if !strings.Contains(listB, "beta work") || !strings.Contains(listB, "beta second") || strings.Contains(listB, "alpha") {
		t.Fatalf("beta's shelf:\n%s", listB)
	}
	if got := gitOutput(t, repo, "stash", "list"); got != "" {
		t.Fatalf("git stash list must stay empty, got %q", got)
	}
	if out, err := gitCmd(repo, "rev-parse", "--verify", "--quiet", "refs/stash").CombinedOutput(); err == nil {
		t.Fatalf("refs/stash must never be created: %s", out)
	}
	if entryCount(t, repo) != 0 {
		t.Fatal("the main checkout's shelf must stay empty")
	}

	mustDo(t, worktreeA, "apply", nil)
	if got := readFile(t, worktreeA, "a.txt"); got != "from alpha\n" {
		t.Fatalf("alpha applied %q", got)
	}
	if _, err := os.Stat(filepath.Join(worktreeA, "b-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("alpha must not receive beta's untracked file: %v", err)
	}

	mustDo(t, worktreeA, "drop", nil)
	if entryCount(t, worktreeA) != 0 || entryCount(t, worktreeB) != 2 {
		t.Fatalf("dropping in alpha changed beta: alpha=%d beta=%d", entryCount(t, worktreeA), entryCount(t, worktreeB))
	}
	if _, err := do(t, worktreeA, "stash", "apply", map[string]any{"stash_ref": "2"}); err == nil {
		t.Fatal("alpha must not resolve beta's entry number")
	}
}

func TestCountShelfEntriesNeedsNoGit(t *testing.T) {
	dir := t.TempDir()
	if n, err := CountShelfEntries(dir); err != nil || n != 0 {
		t.Fatalf("no shelf: %d, %v", n, err)
	}
	for _, name := range []string{"1", "3", ".saving-x", "notes"} {
		if err := os.MkdirAll(filepath.Join(dir, ".spinclass", "stash", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := CountShelfEntries(dir); err != nil || n != 2 {
		t.Fatalf("two numbered entries: %d, %v", n, err)
	}
}

func TestShelfNotEmptyFailsClosed(t *testing.T) {
	wt := t.TempDir()
	if ShelfNotEmpty(wt) {
		t.Fatal("no shelf directory means no shelf")
	}
	if err := os.MkdirAll(filepath.Join(wt, ".spinclass", "stash"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ShelfNotEmpty(wt) {
		t.Fatal("an empty shelf directory is an empty shelf")
	}
	if err := os.MkdirAll(filepath.Join(wt, ".spinclass", "stash", "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !ShelfNotEmpty(wt) {
		t.Fatal("one entry directory is a non-empty shelf")
	}

	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, ".spinclass"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".spinclass", "stash"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ShelfNotEmpty(broken) {
		t.Fatal("an unreadable shelf must read as non-empty")
	}
}

func TestShelfWorksFromASubdirectory(t *testing.T) {
	repo := newShelfRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "a.txt", "two\n")
	mustDo(t, filepath.Join(repo, "sub"), "save", nil)
	if _, err := os.Stat(filepath.Join(repo, ".spinclass", "stash", "1", "meta.json")); err != nil {
		t.Fatalf("the entry belongs at the worktree root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "sub", ".spinclass")); !os.IsNotExist(err) {
		t.Fatalf("no shelf under the subdirectory: %v", err)
	}
}

// Without the exclude (a plain checkout, FDR 0036 section 7) the shelf itself
// must still never be shelved as untracked work.
func TestShelfNeverStashesItself(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "a.txt", "two\n")
	mustDo(t, repo, "save", map[string]any{"include_untracked": true})
	write(t, repo, "a.txt", "three\n")
	mustDo(t, repo, "save", map[string]any{"include_untracked": true})

	meta, err := readMeta(filepath.Join(repo, ".spinclass", "stash", "2"))
	if err != nil || len(meta.Untracked) != 0 {
		t.Fatalf("second entry must hold no shelf files: %+v, %v", meta, err)
	}
}
