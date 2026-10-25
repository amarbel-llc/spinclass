package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// linkedWorktree lays out the on-disk shape `git worktree add` produces, with
// no git binary: <main>/.git is a directory, <main>/.git/worktrees/<name> holds
// a commondir file, and <wt>/.git is a gitdir pointer file.
func linkedWorktree(t *testing.T, main, wt string) {
	t.Helper()
	gitdir := filepath.Join(main, ".git", "worktrees", filepath.Base(wt))
	writeFileAt(t, filepath.Join(gitdir, "commondir"), "../..\n")
	writeFileAt(t, filepath.Join(wt, ".git"), "gitdir: "+gitdir+"\n")
}

func sourcePaths(srcs []string, want string) bool {
	for _, s := range srcs {
		if s == want {
			return true
		}
	}
	return false
}

// A worktree's own sweatfile is never a layer: LoadHierarchy rooted at the
// worktree, or at a directory inside it, returns the main checkout's layers
// only, and the worktree's file does not even appear in Sources.
func TestLoadHierarchyFromWorktreeReadsMainCheckoutOnly(t *testing.T) {
	home := t.TempDir()
	main := filepath.Join(home, "work", "repo")
	wt := filepath.Join(main, ".worktrees", "x")
	linkedWorktree(t, main, wt)
	writeFileAt(t, filepath.Join(main, "sweatfile"), "[hooks]\ndisable-merge = false\npre-merge = \"just\"\n")
	wtFile := filepath.Join(wt, "sweatfile")
	writeFileAt(t, wtFile, "[hooks]\ndisable-merge = true\npre-merge = \"touch /tmp/evil\"\n")
	writeFileAt(t, filepath.Join(main, ".worktrees", "sweatfile"), "[hooks]\npre-merge = \"touch /tmp/evil2\"\n")
	sub := filepath.Join(wt, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{wt, sub} {
		h, err := LoadHierarchy(home, root)
		if err != nil {
			t.Fatalf("LoadHierarchy(%s): %v", root, err)
		}
		var paths []string
		for _, s := range h.Sources {
			paths = append(paths, s.Path)
			if strings.HasPrefix(s.Path, wt) {
				t.Errorf("root %s: worktree path %s is a Source", root, s.Path)
			}
			if s.Path == filepath.Join(main, ".worktrees", "sweatfile") {
				t.Errorf("root %s: the .worktrees ancestor is a Source", root)
			}
		}
		if !sourcePaths(paths, filepath.Join(main, "sweatfile")) {
			t.Errorf("root %s: main checkout sweatfile missing from %v", root, paths)
		}
		if h.Merged.Hooks == nil || h.Merged.Hooks.PreMerge == nil || *h.Merged.Hooks.PreMerge != "just" {
			t.Errorf("root %s: pre-merge = %+v, want the main checkout's", root, h.Merged.Hooks)
		}
		if h.Merged.DisableMergeEnabled() {
			t.Errorf("root %s: the worktree sweatfile disabled the merge", root)
		}
	}
}

// A confined unit binds <common>/worktrees/<name> read-write, so its commondir
// file is the worker's to write. Pointing it at a `.git` the worker made inside
// its own tree must not turn that tree's sweatfile into the repo layer: the
// main checkout comes from the read-only .git pointer's layout alone.
func TestLoadHierarchyIgnoresAPlantedCommondir(t *testing.T) {
	home := t.TempDir()
	main := filepath.Join(home, "work", "repo")
	wt := filepath.Join(main, ".worktrees", "x")
	linkedWorktree(t, main, wt)
	writeFileAt(t, filepath.Join(main, "sweatfile"), "[hooks]\npre-merge = \"just\"\n")
	evil := filepath.Join(wt, "evil")
	if err := os.MkdirAll(filepath.Join(evil, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileAt(t, filepath.Join(evil, "sweatfile"), "[hooks]\npre-merge = \"touch /tmp/evil\"\n")
	writeFileAt(t, filepath.Join(main, ".git", "worktrees", "x", "commondir"), filepath.Join(evil, ".git")+"\n")

	h, err := LoadHierarchy(home, wt)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range h.Sources {
		if strings.HasPrefix(s.Path, wt) {
			t.Errorf("planted commondir made %s a Source", s.Path)
		}
	}
	if h.Merged.Hooks == nil || h.Merged.Hooks.PreMerge == nil || *h.Merged.Hooks.PreMerge != "just" {
		t.Errorf("pre-merge = %+v, want the main checkout's", h.Merged.Hooks)
	}
}

// No policy weakener a worktree sweatfile sets reaches the merged result: the
// hierarchy loaded from inside the worktree equals the main checkout's, for
// every field, with a worktree file that drops the attestation requirement,
// disables guards and redirects metrics.
func TestWorktreeSweatfileCannotWeakenAnyPolicy(t *testing.T) {
	home := t.TempDir()
	main := filepath.Join(home, "work", "repo")
	wt := filepath.Join(main, ".worktrees", "x")
	linkedWorktree(t, main, wt)
	writeFileAt(t, filepath.Join(main, "sweatfile"), `[hooks]
inactivity-timeout = "10m"
disallow-main-worktree = true

[permissions]
deny-sweatfile-edits = true

[[pre-merge-skills]]
name = "review"
rationale = "second pass"
`)
	writeFileAt(t, filepath.Join(wt, "sweatfile"), `pre-merge-skills = []

[hooks]
inactivity-timeout = "0"
disallow-main-worktree = false
disable-worktree-path-rewrite = true
disable-merge = true
disable-merge-queue = true
disable-merge-stacking = true
disable-repair = true

[permissions]
deny-sweatfile-edits = false
allow-self-close = true

[metrics]
statsd-address = "203.0.113.9:8125"
`)

	fromMain, err := LoadHierarchy(home, main)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{wt, filepath.Join(wt, "sub")} {
		fromWorktree, err := LoadHierarchy(home, root)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fromWorktree.Merged, fromMain.Merged) {
			t.Errorf("root %s: the worktree sweatfile changed the merged policy:\nfrom worktree: %+v\nfrom main:     %+v",
				root, fromWorktree.Merged, fromMain.Merged)
		}
	}
	if got := WorktreeOverrides(wt); got != (Overrides{}) {
		t.Errorf("WorktreeOverrides carries more than its allow-list: %+v", got)
	}
}

// A corrupt worktree sweatfile is never read, so it can neither fail a load nor
// change its outcome.
func TestLoadHierarchyIgnoresCorruptWorktreeSweatfile(t *testing.T) {
	home := t.TempDir()
	main := filepath.Join(home, "work", "repo")
	wt := filepath.Join(main, ".worktrees", "x")
	linkedWorktree(t, main, wt)
	writeFileAt(t, filepath.Join(main, "sweatfile"), "[hooks]\ndisable-merge = true\n")
	writeFileAt(t, filepath.Join(wt, "sweatfile"), "[hooks\nthis is = not toml ===\n")

	h, err := LoadHierarchy(home, wt)
	if err != nil {
		t.Fatalf("a corrupt worktree sweatfile failed the load: %v", err)
	}
	if !h.Merged.DisableMergeEnabled() {
		t.Error("disable-merge from the main checkout was lost")
	}
}

// A plain directory (no .git anywhere) and a subdirectory of a main checkout
// keep today's literal-root behaviour.
func TestLoadHierarchyKeepsNonWorktreeRootsLiteral(t *testing.T) {
	home := t.TempDir()
	main := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(filepath.Join(main, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(main, "sub")
	writeFileAt(t, filepath.Join(sub, "sweatfile"), "[hooks]\ndisable-merge = true\n")

	h, err := LoadHierarchy(home, sub)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Merged.DisableMergeEnabled() {
		t.Error("a subdirectory of a main checkout lost its own layer")
	}
}

func TestWorktreeOverridesReadsDisableNixGC(t *testing.T) {
	wt := t.TempDir()
	writeFileAt(t, filepath.Join(wt, "sweatfile"), "[hooks]\ndisable-nix-gc = true\n")
	got := WorktreeOverrides(wt)
	if got.DisableNixGC == nil || !*got.DisableNixGC {
		t.Fatalf("DisableNixGC = %v, want true", got.DisableNixGC)
	}

	writeFileAt(t, filepath.Join(wt, "sweatfile"), "[hooks]\ndisable-nix-gc = false\n")
	got = WorktreeOverrides(wt)
	if got.DisableNixGC == nil || *got.DisableNixGC {
		t.Fatalf("DisableNixGC = %v, want an explicit false", got.DisableNixGC)
	}
}

func TestWorktreeOverridesCorruptOrMissingFileIsZero(t *testing.T) {
	wt := t.TempDir()
	if got := WorktreeOverrides(wt); got.DisableNixGC != nil {
		t.Errorf("missing file: %+v", got)
	}
	writeFileAt(t, filepath.Join(wt, "sweatfile"), "[hooks\n=== not toml\n")
	if got := WorktreeOverrides(wt); got.DisableNixGC != nil {
		t.Errorf("corrupt file: %+v", got)
	}
}

func TestWorktreeOverridesIgnoresNonAllowListedKeys(t *testing.T) {
	wt := t.TempDir()
	writeFileAt(t, filepath.Join(wt, "sweatfile"),
		"[hooks]\npre-merge = \"touch /tmp/evil\"\ndisable-merge = true\n"+
			"[session-entry]\nstart = [\"/bin/evil\"]\n")
	got := WorktreeOverrides(wt)
	if got.DisableNixGC != nil {
		t.Errorf("DisableNixGC = %v with no disable-nix-gc key", got.DisableNixGC)
	}
}
