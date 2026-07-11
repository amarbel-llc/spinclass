package sweatfile_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/sweatfileio"
	"github.com/amarbel-llc/spinclass/internal/testfs"
)

func TestParseMinimal(t *testing.T) {
	input := `
[git]
excludes = [".claude/"]
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Git == nil || len(sf.Git.Excludes) != 1 || sf.Git.Excludes[0] != ".claude/" {
		t.Errorf("git.excludes: got %v", sf.Git)
	}
}

func TestParseEmpty(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Git != nil {
		t.Errorf("expected nil git, got %v", sf.Git)
	}
}

func TestLoadFromPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sweatfile")
	testfs.MustWriteFile(t, path, []byte("[git]\nexcludes = [\".direnv/\"]"), 0o644)

	doc, err := sweatfileio.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Git == nil || len(sf.Git.Excludes) != 1 || sf.Git.Excludes[0] != ".direnv/" {
		t.Errorf("git.excludes: got %v", sf.Git)
	}
}

func TestLoadMissing(t *testing.T) {
	doc, err := sweatfileio.Load("/nonexistent/sweatfile")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Git != nil {
		t.Errorf("expected nil git, got %v", sf.Git)
	}
}

func TestMergeConcatenatesArrays(t *testing.T) {
	base := Sweatfile{
		Git: &Git{Excludes: []string{".claude/"}},
	}
	repo := Sweatfile{
		Git: &Git{Excludes: []string{".direnv/"}},
	}
	merged := base.MergeWith(repo)
	if merged.Git == nil || len(merged.Git.Excludes) != 2 {
		t.Fatalf("expected 2 git.excludes, got %v", merged.Git)
	}
	if merged.Git.Excludes[0] != ".claude/" ||
		merged.Git.Excludes[1] != ".direnv/" {
		t.Errorf("git.excludes: got %v", merged.Git.Excludes)
	}
}

func TestMergeClearSentinel(t *testing.T) {
	base := Sweatfile{
		Git: &Git{Excludes: []string{".claude/"}},
	}
	repo := Sweatfile{
		Git: &Git{Excludes: []string{}},
	}
	merged := base.MergeWith(repo)
	if merged.Git == nil || len(merged.Git.Excludes) != 0 {
		t.Errorf("expected cleared git.excludes, got %v", merged.Git)
	}
}

func TestMergeBaseOnly(t *testing.T) {
	base := Sweatfile{Git: &Git{Excludes: []string{".claude/"}}}
	merged := base.MergeWith(Sweatfile{})
	if merged.Git == nil || len(merged.Git.Excludes) != 1 || merged.Git.Excludes[0] != ".claude/" {
		t.Errorf("expected inherited git.excludes, got %v", merged.Git)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sweatfile")

	input := "[git]\nexcludes = [\".claude/\"]\n"
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = sweatfileio.Save(doc, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loaded, err := sweatfileio.Load(path)
	if err != nil {
		t.Fatalf("unexpected error loading: %v", err)
	}
	sf := loaded.Data()
	if sf.Git == nil || len(sf.Git.Excludes) != 1 || sf.Git.Excludes[0] != ".claude/" {
		t.Errorf("git.excludes roundtrip: got %v", sf.Git)
	}
}

func TestParseClaudeAllow(t *testing.T) {
	input := `
[claude]
allow = ["Read", "Bash(git *)"]
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Claude == nil || len(sf.Claude.Allow) != 2 {
		t.Fatalf("expected 2 claude.allow rules, got %v", sf.Claude)
	}
	if sf.Claude.Allow[0] != "Read" || sf.Claude.Allow[1] != "Bash(git *)" {
		t.Errorf("claude.allow: got %v", sf.Claude.Allow)
	}
}

func TestMergeClaudeAllowAppends(t *testing.T) {
	base := Sweatfile{Claude: &Claude{Allow: []string{"Read", "Glob"}}}
	repo := Sweatfile{Claude: &Claude{Allow: []string{"Bash(go test:*)"}}}
	merged := base.MergeWith(repo)
	if merged.Claude == nil || len(merged.Claude.Allow) != 3 {
		t.Fatalf("expected 3 claude.allow rules, got %v", merged.Claude)
	}
	if merged.Claude.Allow[2] != "Bash(go test:*)" {
		t.Errorf("expected appended rule, got %v", merged.Claude.Allow)
	}
}

func TestMergeClaudeAllowClear(t *testing.T) {
	base := Sweatfile{Claude: &Claude{Allow: []string{"Read", "Glob"}}}
	repo := Sweatfile{Claude: &Claude{Allow: []string{}}}
	merged := base.MergeWith(repo)
	if merged.Claude == nil || len(merged.Claude.Allow) != 0 {
		t.Errorf("expected cleared claude.allow, got %v", merged.Claude)
	}
}

func writeSweatfile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestLoadHierarchyGlobalOnly(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "repos", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	globalPath := filepath.Join(home, ".config", "spinclass", "sweatfile")
	writeSweatfile(t, globalPath, `
[git]
excludes = [".DS_Store"]

[claude]
allow = ["/docs"]
`)

	result, err := sweatfileio.LoadHierarchy(home, repoDir)
	if err != nil {
		t.Fatalf("LoadHierarchy returned error: %v", err)
	}

	// Should have checked: global, eng/sweatfile, eng/repos/sweatfile,
	// myrepo/sweatfile
	if len(result.Sources) != 4 {
		t.Fatalf("expected 4 sources, got %d", len(result.Sources))
	}

	// Only global should be found
	if !result.Sources[0].Found {
		t.Error("expected global source to be found")
	}
	for i := 1; i < len(result.Sources); i++ {
		if result.Sources[i].Found {
			t.Errorf(
				"expected source %d (%s) to not be found",
				i,
				result.Sources[i].Path,
			)
		}
	}

	if result.Merged.Git == nil || len(result.Merged.Git.Excludes) != 1 ||
		result.Merged.Git.Excludes[0] != ".DS_Store" {
		t.Errorf(
			"expected Git.Excludes=[.DS_Store], got %v",
			result.Merged.Git,
		)
	}
	if result.Merged.Claude == nil || len(result.Merged.Claude.Allow) != 1 ||
		result.Merged.Claude.Allow[0] != "/docs" {
		t.Errorf(
			"expected Claude.Allow=[/docs], got %v",
			result.Merged.Claude,
		)
	}
}

func TestLoadHierarchyGlobalAndRepo(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "repos", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	globalPath := filepath.Join(home, ".config", "spinclass", "sweatfile")
	writeSweatfile(t, globalPath, `
[git]
excludes = [".DS_Store"]
`)

	repoSweatfile := filepath.Join(repoDir, "sweatfile")
	writeSweatfile(t, repoSweatfile, `
[git]
excludes = [".idea"]

[claude]
allow = ["/src"]
`)

	result, err := sweatfileio.LoadHierarchy(home, repoDir)
	if err != nil {
		t.Fatalf("LoadHierarchy returned error: %v", err)
	}

	// Merged should have both git.excludes appended
	if result.Merged.Git == nil || len(result.Merged.Git.Excludes) != 2 {
		t.Fatalf("expected 2 Git.Excludes, got %v", result.Merged.Git)
	}
	if result.Merged.Git.Excludes[0] != ".DS_Store" ||
		result.Merged.Git.Excludes[1] != ".idea" {
		t.Errorf(
			"expected Git.Excludes=[.DS_Store, .idea], got %v",
			result.Merged.Git.Excludes,
		)
	}

	// Claude.Allow from repo only
	if result.Merged.Claude == nil || len(result.Merged.Claude.Allow) != 1 ||
		result.Merged.Claude.Allow[0] != "/src" {
		t.Errorf(
			"expected Claude.Allow=[/src], got %v",
			result.Merged.Claude,
		)
	}
}

func TestLoadHierarchyParentDir(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "repos", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	globalPath := filepath.Join(home, ".config", "spinclass", "sweatfile")
	writeSweatfile(t, globalPath, `
[git]
excludes = [".DS_Store"]
`)

	parentPath := filepath.Join(home, "eng", "sweatfile")
	writeSweatfile(t, parentPath, `
[git]
excludes = [".envrc"]

[claude]
allow = ["/eng-docs"]
`)

	repoSweatfile := filepath.Join(repoDir, "sweatfile")
	writeSweatfile(t, repoSweatfile, `
[claude]
allow = ["/src"]
`)

	result, err := sweatfileio.LoadHierarchy(home, repoDir)
	if err != nil {
		t.Fatalf("LoadHierarchy returned error: %v", err)
	}

	// git.excludes: global .DS_Store + parent .envrc = [.DS_Store, .envrc]
	// repo has nil git so inherits
	if result.Merged.Git == nil || len(result.Merged.Git.Excludes) != 2 {
		t.Fatalf("expected 2 Git.Excludes, got %v", result.Merged.Git)
	}
	if result.Merged.Git.Excludes[0] != ".DS_Store" ||
		result.Merged.Git.Excludes[1] != ".envrc" {
		t.Errorf(
			"expected Git.Excludes=[.DS_Store, .envrc], got %v",
			result.Merged.Git.Excludes,
		)
	}

	// claude.allow: parent /eng-docs + repo /src = [/eng-docs, /src]
	if result.Merged.Claude == nil || len(result.Merged.Claude.Allow) != 2 {
		t.Fatalf("expected 2 Claude.Allow, got %v", result.Merged.Claude)
	}
	if result.Merged.Claude.Allow[0] != "/eng-docs" ||
		result.Merged.Claude.Allow[1] != "/src" {
		t.Errorf(
			"expected Claude.Allow=[/eng-docs, /src], got %v",
			result.Merged.Claude.Allow,
		)
	}

	// Verify sources: global found, eng/sweatfile found, eng/repos/sweatfile
	// not found, myrepo/sweatfile found
	if !result.Sources[0].Found {
		t.Error("expected global source to be found")
	}
	if !result.Sources[1].Found {
		t.Error("expected eng/sweatfile source to be found")
	}
	if result.Sources[2].Found {
		t.Error("expected eng/repos/sweatfile source to not be found")
	}
	if !result.Sources[3].Found {
		t.Error("expected repo sweatfile source to be found")
	}
}

func TestLoadHierarchyNoSweatfiles(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "repos", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := sweatfileio.LoadHierarchy(home, repoDir)
	if err != nil {
		t.Fatalf("LoadHierarchy returned error: %v", err)
	}

	// All sources should be not found
	for i, src := range result.Sources {
		if src.Found {
			t.Errorf("expected source %d (%s) to not be found", i, src.Path)
		}
	}

	// Merged should be empty
	if result.Merged.Git != nil {
		t.Errorf("expected nil Git, got %v", result.Merged.Git)
	}
	if result.Merged.Claude != nil {
		t.Errorf("expected nil Claude, got %v", result.Merged.Claude)
	}
}

// TestLoadHierarchyOutOfHomeChain exercises flavor (b) of #72: when repoDir
// is not under $HOME, the parent walk continues all the way up to the
// filesystem root (exclusive). A sweatfile placed above $HOME but below /
// must be picked up.
func TestLoadHierarchyOutOfHomeChain(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(base, "external", "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Out-of-home ancestor: base/sweatfile. Walking parents of repoDir
	// without the home bound: base/external, base.
	parentPath := filepath.Join(base, "sweatfile")
	writeSweatfile(t, parentPath, `
[git]
excludes = ["external-marker"]
`)

	result, err := sweatfileio.LoadHierarchy(home, repoDir)
	if err != nil {
		t.Fatalf("LoadHierarchy returned error: %v", err)
	}

	if result.Merged.Git == nil ||
		len(result.Merged.Git.Excludes) != 1 ||
		result.Merged.Git.Excludes[0] != "external-marker" {
		t.Errorf(
			"expected base/sweatfile merged into Git.Excludes, got %v",
			result.Merged.Git,
		)
	}

	// Sources should include the base/sweatfile entry as found.
	var sawBase bool
	for _, src := range result.Sources {
		if src.Path == parentPath && src.Found {
			sawBase = true
		}
	}
	if !sawBase {
		var paths []string
		for _, src := range result.Sources {
			paths = append(paths, src.Path)
		}
		t.Errorf("expected sources to include %q, got %v", parentPath, paths)
	}
}

// TestLoadHierarchyDualChainViaSymlink exercises the two-chain walk: when
// repoDir is a symlinked path, sweatfiles found only in the realpath chain
// must still merge.
func TestLoadHierarchyDualChainViaSymlink(t *testing.T) {
	home := t.TempDir()

	canonicalRepo := filepath.Join(home, "canonical", "repo")
	if err := os.MkdirAll(canonicalRepo, 0o755); err != nil {
		t.Fatal(err)
	}

	symlinkParent := filepath.Join(home, "eng-acme", "repos")
	if err := os.MkdirAll(symlinkParent, 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkRepo := filepath.Join(symlinkParent, "repo")
	if err := os.Symlink(canonicalRepo, symlinkRepo); err != nil {
		t.Fatal(err)
	}

	// Reachable only via lexical (symlinked) chain.
	engAcmePath := filepath.Join(home, "eng-acme", "sweatfile")
	writeSweatfile(t, engAcmePath, `
[git]
excludes = ["from-eng-acme"]
`)

	// Reachable only via realpath chain.
	canonicalParentPath := filepath.Join(home, "canonical", "sweatfile")
	writeSweatfile(t, canonicalParentPath, `
[git]
excludes = ["from-canonical"]
`)

	result, err := sweatfileio.LoadHierarchy(home, symlinkRepo)
	if err != nil {
		t.Fatalf("LoadHierarchy returned error: %v", err)
	}

	got := []string{}
	if result.Merged.Git != nil {
		got = result.Merged.Git.Excludes
	}
	var foundEngAcme, foundCanonical bool
	for _, e := range got {
		switch e {
		case "from-eng-acme":
			foundEngAcme = true
		case "from-canonical":
			foundCanonical = true
		}
	}
	if !foundEngAcme || !foundCanonical {
		t.Errorf(
			"expected both lexical and realpath chains merged, got Git.Excludes=%v",
			got,
		)
	}
}

// TestLoadHierarchyConvergentChainDedup exercises the dedup decision: when
// the lexical and realpath chains converge on the same canonical directory,
// the sweatfile is loaded once and the second chain's entry is recorded
// with SkipReason set.
func TestLoadHierarchyConvergentChainDedup(t *testing.T) {
	home := t.TempDir()

	canonicalMid := filepath.Join(home, "canonical")
	if err := os.MkdirAll(canonicalMid, 0o755); err != nil {
		t.Fatal(err)
	}

	aliasMid := filepath.Join(home, "alias")
	if err := os.Symlink(canonicalMid, aliasMid); err != nil {
		t.Fatal(err)
	}

	repoCanonical := filepath.Join(canonicalMid, "repo")
	if err := os.MkdirAll(repoCanonical, 0o755); err != nil {
		t.Fatal(err)
	}

	// Place sweatfile at canonical mid-tree dir; both chains will see it.
	canonicalPath := filepath.Join(canonicalMid, "sweatfile")
	writeSweatfile(t, canonicalPath, `
[git]
excludes = ["once-only"]
`)

	repoViaAlias := filepath.Join(aliasMid, "repo")
	result, err := sweatfileio.LoadHierarchy(home, repoViaAlias)
	if err != nil {
		t.Fatalf("LoadHierarchy returned error: %v", err)
	}

	// `once-only` should appear exactly once.
	if result.Merged.Git == nil || len(result.Merged.Git.Excludes) != 1 ||
		result.Merged.Git.Excludes[0] != "once-only" {
		t.Errorf(
			"expected Git.Excludes=[once-only], got %v",
			result.Merged.Git,
		)
	}

	// One source loaded, one source skipped — both for the canonical
	// mid-tree dir.
	var loadedCount, skippedCount int
	for _, src := range result.Sources {
		dir := filepath.Dir(src.Path)
		canon, err := filepath.EvalSymlinks(dir)
		if err != nil {
			canon = dir
		}
		if canon != canonicalMid {
			continue
		}
		if src.SkipReason != "" {
			skippedCount++
		} else if src.Found {
			loadedCount++
		}
	}
	if loadedCount != 1 {
		t.Errorf("expected canonical sweatfile loaded once, got %d (sources: %+v)",
			loadedCount, result.Sources)
	}
	if skippedCount != 1 {
		t.Errorf("expected canonical sweatfile skipped once via dedup, got %d (sources: %+v)",
			skippedCount, result.Sources)
	}
}

func TestParseHooksCreate(t *testing.T) {
	input := `
[hooks]
create = "composer install"
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks == nil || sf.Hooks.Create == nil ||
		*sf.Hooks.Create != "composer install" {
		t.Errorf("hooks.create: got %v", sf.Hooks)
	}
}

func TestParseHooksStop(t *testing.T) {
	input := `
[hooks]
stop = "just test"
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks == nil || sf.Hooks.Stop == nil ||
		*sf.Hooks.Stop != "just test" {
		t.Errorf("hooks.stop: got %v", sf.Hooks)
	}
}

func TestParseHooksBoth(t *testing.T) {
	input := `
[hooks]
create = "npm install"
stop = "just lint"
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks == nil {
		t.Fatal("expected non-nil hooks")
	}
	if sf.Hooks.Create == nil || *sf.Hooks.Create != "npm install" {
		t.Errorf("hooks.create: got %v", sf.Hooks.Create)
	}
	if sf.Hooks.Stop == nil || *sf.Hooks.Stop != "just lint" {
		t.Errorf("hooks.stop: got %v", sf.Hooks.Stop)
	}
}

func TestParseHooksAbsent(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks != nil {
		t.Errorf("expected nil hooks, got %v", sf.Hooks)
	}
}

func TestMergeHooksCreateInherit(t *testing.T) {
	cmd := "npm install"
	base := Sweatfile{Hooks: &Hooks{Create: &cmd}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.Create == nil ||
		*merged.Hooks.Create != "npm install" {
		t.Errorf("expected inherited hooks.create, got %v", merged.Hooks)
	}
}

func TestMergeHooksCreateOverride(t *testing.T) {
	baseCmd := "npm install"
	repoCmd := "composer install"
	base := Sweatfile{Hooks: &Hooks{Create: &baseCmd}}
	repo := Sweatfile{Hooks: &Hooks{Create: &repoCmd}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.Create == nil ||
		*merged.Hooks.Create != "composer install" {
		t.Errorf("expected overridden hooks.create, got %v", merged.Hooks)
	}
}

func TestMergeHooksCreateClear(t *testing.T) {
	baseCmd := "npm install"
	empty := ""
	base := Sweatfile{Hooks: &Hooks{Create: &baseCmd}}
	repo := Sweatfile{Hooks: &Hooks{Create: &empty}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.Create == nil ||
		*merged.Hooks.Create != "" {
		t.Errorf("expected cleared hooks.create, got %v", merged.Hooks)
	}
}

func TestMergeHooksStopInherit(t *testing.T) {
	cmd := "just test"
	base := Sweatfile{Hooks: &Hooks{Stop: &cmd}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.Stop == nil ||
		*merged.Hooks.Stop != "just test" {
		t.Errorf("expected inherited hooks.stop, got %v", merged.Hooks)
	}
}

func TestMergeHooksStopOverride(t *testing.T) {
	baseCmd := "just test"
	repoCmd := "just lint"
	base := Sweatfile{Hooks: &Hooks{Stop: &baseCmd}}
	repo := Sweatfile{Hooks: &Hooks{Stop: &repoCmd}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.Stop == nil ||
		*merged.Hooks.Stop != "just lint" {
		t.Errorf("expected overridden hooks.stop, got %v", merged.Hooks)
	}
}

func TestMergeHooksStopClear(t *testing.T) {
	baseCmd := "just test"
	empty := ""
	base := Sweatfile{Hooks: &Hooks{Stop: &baseCmd}}
	repo := Sweatfile{Hooks: &Hooks{Stop: &empty}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.Stop == nil ||
		*merged.Hooks.Stop != "" {
		t.Errorf("expected cleared hooks.stop, got %v", merged.Hooks)
	}
}

func TestMergeHooksIndependentFields(t *testing.T) {
	createCmd := "npm install"
	stopCmd := "just test"
	base := Sweatfile{Hooks: &Hooks{Create: &createCmd}}
	repo := Sweatfile{Hooks: &Hooks{Stop: &stopCmd}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil {
		t.Fatal("expected non-nil hooks")
	}
	if merged.Hooks.Create == nil || *merged.Hooks.Create != "npm install" {
		t.Errorf("expected inherited hooks.create, got %v", merged.Hooks.Create)
	}
	if merged.Hooks.Stop == nil || *merged.Hooks.Stop != "just test" {
		t.Errorf("expected overridden hooks.stop, got %v", merged.Hooks.Stop)
	}
}

func TestParseHooksPreMerge(t *testing.T) {
	input := `
[hooks]
pre-merge = "just test"
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks == nil || sf.Hooks.PreMerge == nil ||
		*sf.Hooks.PreMerge != "just test" {
		t.Errorf("hooks.pre-merge: got %v", sf.Hooks)
	}
}

func TestMergeHooksPreMergeInherit(t *testing.T) {
	cmd := "just test"
	base := Sweatfile{Hooks: &Hooks{PreMerge: &cmd}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.PreMerge == nil ||
		*merged.Hooks.PreMerge != "just test" {
		t.Errorf("expected inherited hooks.pre-merge, got %v", merged.Hooks)
	}
}

func TestMergeHooksPreMergeOverride(t *testing.T) {
	baseCmd := "just test"
	repoCmd := "just lint"
	base := Sweatfile{Hooks: &Hooks{PreMerge: &baseCmd}}
	repo := Sweatfile{Hooks: &Hooks{PreMerge: &repoCmd}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.PreMerge == nil ||
		*merged.Hooks.PreMerge != "just lint" {
		t.Errorf("expected overridden hooks.pre-merge, got %v", merged.Hooks)
	}
}

func TestMergeHooksPreMergeClear(t *testing.T) {
	baseCmd := "just test"
	empty := ""
	base := Sweatfile{Hooks: &Hooks{PreMerge: &baseCmd}}
	repo := Sweatfile{Hooks: &Hooks{PreMerge: &empty}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.PreMerge == nil ||
		*merged.Hooks.PreMerge != "" {
		t.Errorf("expected cleared hooks.pre-merge, got %v", merged.Hooks)
	}
}

func TestParseHooksPreMergeOutputFormat(t *testing.T) {
	input := `
[hooks]
pre-merge-output-format = "tap-ndjson"
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks == nil || sf.Hooks.PreMergeOutputFormat == nil ||
		*sf.Hooks.PreMergeOutputFormat != "tap-ndjson" {
		t.Errorf("hooks.pre-merge-output-format: got %v", sf.Hooks)
	}
}

func TestMergeHooksPreMergeOutputFormatInherit(t *testing.T) {
	format := "tap-ndjson"
	base := Sweatfile{Hooks: &Hooks{PreMergeOutputFormat: &format}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.PreMergeOutputFormat == nil ||
		*merged.Hooks.PreMergeOutputFormat != "tap-ndjson" {
		t.Errorf("expected inherited hooks.pre-merge-output-format, got %v", merged.Hooks)
	}
}

func TestMergeHooksPreMergeOutputFormatOverride(t *testing.T) {
	baseFmt := "tap-ndjson"
	repoFmt := "raw"
	base := Sweatfile{Hooks: &Hooks{PreMergeOutputFormat: &baseFmt}}
	repo := Sweatfile{Hooks: &Hooks{PreMergeOutputFormat: &repoFmt}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.PreMergeOutputFormat == nil ||
		*merged.Hooks.PreMergeOutputFormat != "raw" {
		t.Errorf("expected overridden hooks.pre-merge-output-format, got %v", merged.Hooks)
	}
}

func TestMergeHooksPreMergeOutputFormatClear(t *testing.T) {
	baseFmt := "tap-ndjson"
	empty := ""
	base := Sweatfile{Hooks: &Hooks{PreMergeOutputFormat: &baseFmt}}
	repo := Sweatfile{Hooks: &Hooks{PreMergeOutputFormat: &empty}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.PreMergeOutputFormat == nil ||
		*merged.Hooks.PreMergeOutputFormat != "" {
		t.Errorf("expected cleared hooks.pre-merge-output-format, got %v", merged.Hooks)
	}
}

func TestPreMergeOutputFormatValueDefault(t *testing.T) {
	// nil Hooks → "raw"
	sf := Sweatfile{}
	if got := sf.PreMergeOutputFormatValue(); got != "raw" {
		t.Errorf("nil Hooks: got %q, want %q", got, "raw")
	}

	// nil PreMergeOutputFormat → "raw"
	sf = Sweatfile{Hooks: &Hooks{}}
	if got := sf.PreMergeOutputFormatValue(); got != "raw" {
		t.Errorf("nil PreMergeOutputFormat: got %q, want %q", got, "raw")
	}

	// empty PreMergeOutputFormat → "raw"
	empty := ""
	sf = Sweatfile{Hooks: &Hooks{PreMergeOutputFormat: &empty}}
	if got := sf.PreMergeOutputFormatValue(); got != "raw" {
		t.Errorf("empty PreMergeOutputFormat: got %q, want %q", got, "raw")
	}

	// non-empty PreMergeOutputFormat → that value
	val := "tap-ndjson"
	sf = Sweatfile{Hooks: &Hooks{PreMergeOutputFormat: &val}}
	if got := sf.PreMergeOutputFormatValue(); got != "tap-ndjson" {
		t.Errorf("set PreMergeOutputFormat: got %q, want %q", got, "tap-ndjson")
	}
}

func TestLoadHierarchyRepoOverridesParent(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "repos", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	parentPath := filepath.Join(home, "eng", "sweatfile")
	writeSweatfile(t, parentPath, `
[git]
excludes = [".DS_Store", ".envrc"]

[claude]
allow = ["/docs"]
`)

	// Repo sweatfile with empty arrays clears parent values
	repoSweatfile := filepath.Join(repoDir, "sweatfile")
	writeSweatfile(t, repoSweatfile, `
[git]
excludes = []

[claude]
allow = []
`)

	result, err := sweatfileio.LoadHierarchy(home, repoDir)
	if err != nil {
		t.Fatalf("LoadHierarchy returned error: %v", err)
	}

	// Empty arrays should clear parent values
	if result.Merged.Git == nil ||
		len(result.Merged.Git.Excludes) != 0 {
		t.Errorf(
			"expected empty Git.Excludes (cleared by repo), got %v",
			result.Merged.Git,
		)
	}
	if result.Merged.Claude == nil || len(result.Merged.Claude.Allow) != 0 {
		t.Errorf(
			"expected empty Claude.Allow (cleared by repo), got %v",
			result.Merged.Claude,
		)
	}
}

func TestLoadHierarchyHooksStopInherited(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "repos", "myrepo")
	testfs.MustMkdirAll(t, repoDir, 0o755)

	globalPath := filepath.Join(home, ".config", "spinclass", "sweatfile")
	writeSweatfile(t, globalPath, "[hooks]\nstop = \"just test\"")

	result, err := sweatfileio.LoadHierarchy(home, repoDir)
	if err != nil {
		t.Fatalf("LoadHierarchy returned error: %v", err)
	}

	if result.Merged.StopHookCommand() == nil ||
		*result.Merged.StopHookCommand() != "just test" {
		t.Errorf("expected inherited hooks.stop, got %v", result.Merged.Hooks)
	}
}

func TestParseEnvrcDirectives(t *testing.T) {
	input := "[direnv]\nenvrc = [\"source_up\", \"dotenv_if_exists\"]"
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Direnv == nil || len(sf.Direnv.Envrc) != 2 {
		t.Fatalf("expected 2 direnv.envrc, got %v", sf.Direnv)
	}
	if sf.Direnv.Envrc[0] != "source_up" ||
		sf.Direnv.Envrc[1] != "dotenv_if_exists" {
		t.Errorf("direnv.envrc: got %v", sf.Direnv.Envrc)
	}
}

func TestParseEnvrcDirectivesAbsent(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Direnv != nil {
		t.Errorf("expected nil direnv, got %v", sf.Direnv)
	}
}

func TestMergeEnvrcDirectivesAppend(t *testing.T) {
	base := Sweatfile{Direnv: &Direnv{Envrc: []string{"source_up"}}}
	repo := Sweatfile{Direnv: &Direnv{Envrc: []string{"dotenv_if_exists"}}}
	merged := base.MergeWith(repo)
	if merged.Direnv == nil || len(merged.Direnv.Envrc) != 2 {
		t.Fatalf("expected 2 direnv.envrc, got %v", merged.Direnv)
	}
}

func TestMergeEnvrcDirectivesClear(t *testing.T) {
	base := Sweatfile{Direnv: &Direnv{Envrc: []string{"source_up"}}}
	repo := Sweatfile{Direnv: &Direnv{Envrc: []string{}}}
	merged := base.MergeWith(repo)
	if merged.Direnv == nil || len(merged.Direnv.Envrc) != 0 {
		t.Errorf(
			"expected cleared direnv.envrc, got %v",
			merged.Direnv,
		)
	}
}

func TestMergeEnvrcDirectivesInherit(t *testing.T) {
	base := Sweatfile{Direnv: &Direnv{Envrc: []string{"source_up"}}}
	merged := base.MergeWith(Sweatfile{})
	if merged.Direnv == nil || len(merged.Direnv.Envrc) != 1 ||
		merged.Direnv.Envrc[0] != "source_up" {
		t.Errorf(
			"expected inherited direnv.envrc, got %v",
			merged.Direnv,
		)
	}
}

func TestParseEnv(t *testing.T) {
	input := `
[direnv]

[direnv.dotenv]
FOO = "bar"
BAZ = "qux"
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Direnv == nil || len(sf.Direnv.Dotenv) != 2 {
		t.Fatalf("expected 2 env vars, got %v", sf.Direnv)
	}
	if sf.Direnv.Dotenv["FOO"] != "bar" || sf.Direnv.Dotenv["BAZ"] != "qux" {
		t.Errorf("direnv.dotenv: got %v", sf.Direnv.Dotenv)
	}
}

func TestParseEnvAbsent(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Direnv != nil {
		t.Errorf("expected nil direnv, got %v", sf.Direnv)
	}
}

func TestMergeEnvInherit(t *testing.T) {
	base := Sweatfile{Direnv: &Direnv{Dotenv: map[string]string{"FOO": "bar"}}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if merged.Direnv == nil || merged.Direnv.Dotenv["FOO"] != "bar" {
		t.Errorf("expected inherited env, got %v", merged.Direnv)
	}
}

func TestMergeEnvOverrideKey(t *testing.T) {
	base := Sweatfile{Direnv: &Direnv{Dotenv: map[string]string{"FOO": "bar", "BAZ": "qux"}}}
	repo := Sweatfile{Direnv: &Direnv{Dotenv: map[string]string{"FOO": "override"}}}
	merged := base.MergeWith(repo)
	if merged.Direnv == nil || merged.Direnv.Dotenv["FOO"] != "override" {
		t.Errorf("expected overridden FOO, got %v", merged.Direnv)
	}
	if merged.Direnv.Dotenv["BAZ"] != "qux" {
		t.Errorf("expected inherited BAZ, got %v", merged.Direnv.Dotenv["BAZ"])
	}
}

func TestMergeEnvAddKey(t *testing.T) {
	base := Sweatfile{Direnv: &Direnv{Dotenv: map[string]string{"FOO": "bar"}}}
	repo := Sweatfile{Direnv: &Direnv{Dotenv: map[string]string{"BAZ": "qux"}}}
	merged := base.MergeWith(repo)
	if merged.Direnv == nil || len(merged.Direnv.Dotenv) != 2 {
		t.Fatalf("expected 2 env vars, got %v", merged.Direnv)
	}
}

func TestLoadHierarchyHooksStopOverriddenByRepo(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "repos", "myrepo")
	testfs.MustMkdirAll(t, repoDir, 0o755)

	globalPath := filepath.Join(home, ".config", "spinclass", "sweatfile")
	writeSweatfile(t, globalPath, "[hooks]\nstop = \"just test\"")

	repoSweatfile := filepath.Join(repoDir, "sweatfile")
	writeSweatfile(t, repoSweatfile, "[hooks]\nstop = \"just lint\"")

	result, err := sweatfileio.LoadHierarchy(home, repoDir)
	if err != nil {
		t.Fatalf("LoadHierarchy returned error: %v", err)
	}

	if result.Merged.StopHookCommand() == nil ||
		*result.Merged.StopHookCommand() != "just lint" {
		t.Errorf("expected overridden hooks.stop, got %v", result.Merged.Hooks)
	}
}

func TestParseHooksDisallowMainWorktree(t *testing.T) {
	input := `
[hooks]
disallow-main-worktree = true
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if !sf.DisallowMainWorktreeEnabled() {
		t.Error("expected disallow-main-worktree to be enabled")
	}
}

func TestParseHooksDisallowMainWorktreeAbsent(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.DisallowMainWorktreeEnabled() {
		t.Error("expected disallow-main-worktree to be disabled when absent")
	}
}

func TestMergeDisallowMainWorktreeInherit(t *testing.T) {
	enabled := true
	base := Sweatfile{Hooks: &Hooks{DisallowMainWorktree: &enabled}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if !merged.DisallowMainWorktreeEnabled() {
		t.Error("expected inherited disallow-main-worktree")
	}
}

func TestMergeDisallowMainWorktreeOverride(t *testing.T) {
	enabled := true
	disabled := false
	base := Sweatfile{Hooks: &Hooks{DisallowMainWorktree: &enabled}}
	repo := Sweatfile{Hooks: &Hooks{DisallowMainWorktree: &disabled}}
	merged := base.MergeWith(repo)
	if merged.DisallowMainWorktreeEnabled() {
		t.Error("expected overridden disallow-main-worktree to be disabled")
	}
}

func TestLoadWorktreeHierarchyMainRepoSweatfileIncluded(t *testing.T) {
	home := t.TempDir()
	mainRepo := filepath.Join(home, "eng", "repos", "myrepo")
	worktreeDir := filepath.Join(mainRepo, ".worktrees", "my-branch")
	testfs.MustMkdirAll(t, worktreeDir, 0o755)

	// Main repo sweatfile enables disallow-main-worktree
	writeSweatfile(t, filepath.Join(mainRepo, "sweatfile"),
		"[hooks]\ndisallow-main-worktree = true\n")

	result, err := sweatfileio.LoadWorktreeHierarchy(home, mainRepo, worktreeDir)
	if err != nil {
		t.Fatalf("LoadWorktreeHierarchy returned error: %v", err)
	}

	if !result.Merged.DisallowMainWorktreeEnabled() {
		t.Error("expected disallow-main-worktree from main repo sweatfile")
	}
}

func TestLoadWorktreeHierarchyWorktreeOverridesMainRepo(t *testing.T) {
	home := t.TempDir()
	mainRepo := filepath.Join(home, "eng", "repos", "myrepo")
	worktreeDir := filepath.Join(mainRepo, ".worktrees", "my-branch")
	testfs.MustMkdirAll(t, worktreeDir, 0o755)

	// Main repo enables it
	writeSweatfile(t, filepath.Join(mainRepo, "sweatfile"),
		"[hooks]\ndisallow-main-worktree = true\n")

	// Worktree disables it
	writeSweatfile(t, filepath.Join(worktreeDir, "sweatfile"),
		"[hooks]\ndisallow-main-worktree = false\n")

	result, err := sweatfileio.LoadWorktreeHierarchy(home, mainRepo, worktreeDir)
	if err != nil {
		t.Fatalf("LoadWorktreeHierarchy returned error: %v", err)
	}

	if result.Merged.DisallowMainWorktreeEnabled() {
		t.Error("expected worktree sweatfile to override main repo")
	}
}

func TestMergeToolUseLogInherit(t *testing.T) {
	enabled := true
	base := Sweatfile{Hooks: &Hooks{ToolUseLog: &enabled}}
	overlay := Sweatfile{}
	merged := base.MergeWith(overlay)
	if !merged.ToolUseLogEnabled() {
		t.Error("expected ToolUseLog to be inherited")
	}
}

func TestMergeToolUseLogOverride(t *testing.T) {
	enabled := true
	disabled := false
	base := Sweatfile{Hooks: &Hooks{ToolUseLog: &enabled}}
	overlay := Sweatfile{Hooks: &Hooks{ToolUseLog: &disabled}}
	merged := base.MergeWith(overlay)
	if merged.ToolUseLogEnabled() {
		t.Error("expected ToolUseLog to be overridden to false")
	}
}

func TestParseToolUseLog(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[hooks]\ntool-use-log = true\n"))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !doc.Data().ToolUseLogEnabled() {
		t.Error("expected ToolUseLog to be true")
	}
	undecoded := doc.Undecoded()
	for _, key := range undecoded {
		if key == "hooks.tool-use-log" {
			t.Error("tool-use-log should be decoded, not undecoded")
		}
	}
}

func TestRoundTripPreservesComments(t *testing.T) {
	input := `# Global config

[git]
excludes = [".claude/", ".direnv/"]

[claude]
allow = ["Bash(git *)"]

[direnv]
envrc = ["source_up", "use flake"]

[direnv.dotenv]
FOO = "bar"

[hooks]
# install deps on create
create = "npm install"
stop = "just test"
disallow-main-worktree = true
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	output, err := doc.Encode()
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}

	if string(output) != input {
		t.Errorf("round-trip mismatch:\n--- want ---\n%s\n--- got ---\n%s", input, string(output))
	}
}

func TestParseSessionTable(t *testing.T) {
	input := `
[session-entry]
start = ["zellij", "-s", "test"]
resume = ["zellij", "attach", "test"]
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	sf := doc.Data()
	if sf.SessionEntry == nil {
		t.Fatal("expected SessionEntry to be non-nil")
	}
	if len(sf.SessionEntry.Start) != 3 || sf.SessionEntry.Start[0] != "zellij" {
		t.Errorf("Start = %v, want [zellij -s test]", sf.SessionEntry.Start)
	}
	if len(sf.SessionEntry.Resume) != 3 || sf.SessionEntry.Resume[0] != "zellij" {
		t.Errorf("Resume = %v, want [zellij attach test]", sf.SessionEntry.Resume)
	}
}

func TestParseSessionDefault(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	sf := doc.Data()
	if sf.SessionEntry != nil {
		t.Error("expected SessionEntry to be nil for empty sweatfile")
	}
}

func TestSessionAccessorDefaults(t *testing.T) {
	sf := Sweatfile{}
	start := sf.SessionStart()
	if len(start) != 1 {
		t.Fatalf("expected 1-element default start, got %v", start)
	}
	resume := sf.SessionResume()
	if resume != nil {
		t.Errorf("expected nil resume, got %v", resume)
	}
}

func TestMergeSessionOverride(t *testing.T) {
	base := Sweatfile{
		SessionEntry: &SessionEntry{
			Start:  []string{"bash"},
			Resume: []string{"tmux", "attach"},
		},
	}
	override := Sweatfile{
		SessionEntry: &SessionEntry{
			Start: []string{"zellij"},
		},
	}
	merged := base.MergeWith(override)
	if merged.SessionEntry == nil {
		t.Fatal("expected SessionEntry to be non-nil after merge")
	}
	if len(merged.SessionEntry.Start) != 1 || merged.SessionEntry.Start[0] != "zellij" {
		t.Errorf("Start = %v, want [zellij]", merged.SessionEntry.Start)
	}
	if len(merged.SessionEntry.Resume) != 2 || merged.SessionEntry.Resume[0] != "tmux" {
		t.Errorf("Resume = %v, want [tmux attach]", merged.SessionEntry.Resume)
	}
}

func TestMergeSessionNilInherit(t *testing.T) {
	base := Sweatfile{
		SessionEntry: &SessionEntry{Start: []string{"fish"}},
	}
	override := Sweatfile{}
	merged := base.MergeWith(override)
	if merged.SessionEntry == nil || len(merged.SessionEntry.Start) != 1 || merged.SessionEntry.Start[0] != "fish" {
		t.Errorf("expected SessionEntry.Start to be inherited, got %v", merged.SessionEntry)
	}
}

func TestParseSessionEntrySpawnEntry(t *testing.T) {
	input := `
[session-entry]
spawn-entry = ["clown", "--clown-attach=spawn", "--", "{prompt}"]
spawn-window = ["sc-spawn-window", "{id}", "{dir}"]
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	sf := doc.Data()
	if sf.SessionEntry == nil {
		t.Fatal("expected SessionEntry to be non-nil")
	}
	if len(sf.SessionEntry.SpawnEntry) != 4 || sf.SessionEntry.SpawnEntry[0] != "clown" ||
		sf.SessionEntry.SpawnEntry[3] != "{prompt}" {
		t.Errorf("SpawnEntry = %v, want [clown --clown-attach=spawn -- {prompt}]", sf.SessionEntry.SpawnEntry)
	}
	if len(sf.SessionEntry.SpawnWindow) != 3 || sf.SessionEntry.SpawnWindow[0] != "sc-spawn-window" {
		t.Errorf("SpawnWindow = %v, want [sc-spawn-window {id} {dir}]", sf.SessionEntry.SpawnWindow)
	}
}

func TestSessionSpawnEntryAccessorDefault(t *testing.T) {
	// FDR-0017 Piece 1: spawn-entry defaults to the clown spawn form, exec'd
	// directly (no zmx wrap — clown self-detaches via --clown-attach=spawn).
	want := []string{"clown", "--clown-attach=spawn", "--", "{prompt}"}
	for _, sf := range []Sweatfile{
		{},
		{SessionEntry: &SessionEntry{}},
	} {
		got := sf.SessionSpawnEntry()
		if len(got) != len(want) {
			t.Fatalf("SessionSpawnEntry() = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("SessionSpawnEntry() = %v, want %v", got, want)
			}
		}
	}
}

func TestSessionSpawnEntryAccessorConfigured(t *testing.T) {
	sf := Sweatfile{
		SessionEntry: &SessionEntry{
			SpawnEntry: []string{"my-harness", "{prompt}"},
		},
	}
	got := sf.SessionSpawnEntry()
	if len(got) != 2 || got[0] != "my-harness" || got[1] != "{prompt}" {
		t.Errorf("SessionSpawnEntry() = %v, want [my-harness {prompt}]", got)
	}
}

func TestSessionSpawnWindowAccessor(t *testing.T) {
	for _, sf := range []Sweatfile{{}, {SessionEntry: &SessionEntry{}}} {
		if got := sf.SessionSpawnWindow(); got != nil {
			t.Errorf("SessionSpawnWindow() = %v, want nil (no default)", got)
		}
	}
	sf := Sweatfile{SessionEntry: &SessionEntry{
		SpawnWindow: []string{"sc-spawn-window", "{id}", "{dir}"},
	}}
	if got := sf.SessionSpawnWindow(); len(got) != 3 || got[0] != "sc-spawn-window" {
		t.Errorf("SessionSpawnWindow() = %v, want configured argv", got)
	}
}

func TestMergeSessionSpawnWindow(t *testing.T) {
	base := Sweatfile{SessionEntry: &SessionEntry{
		SpawnWindow: []string{"old", "{id}"},
	}}
	override := Sweatfile{SessionEntry: &SessionEntry{
		SpawnWindow: []string{"new", "{id}", "{dir}"},
	}}
	merged := base.MergeWith(override)
	if len(merged.SessionEntry.SpawnWindow) != 3 || merged.SessionEntry.SpawnWindow[0] != "new" {
		t.Errorf("SpawnWindow = %v, want override", merged.SessionEntry.SpawnWindow)
	}
}

func TestMergeSessionSpawnEntryOverrideAndInherit(t *testing.T) {
	base := Sweatfile{
		SessionEntry: &SessionEntry{
			SpawnEntry:  []string{"clown", "--clown-attach=spawn", "--", "{prompt}"},
			SpawnWindow: []string{"sc-spawn-window", "{id}"},
		},
	}
	// Override spawn-entry, inherit spawn-window.
	override := Sweatfile{
		SessionEntry: &SessionEntry{
			SpawnEntry: []string{"other-harness", "{prompt}"},
		},
	}
	merged := base.MergeWith(override)
	if merged.SessionEntry == nil {
		t.Fatal("expected SessionEntry to be non-nil after merge")
	}
	if len(merged.SessionEntry.SpawnEntry) != 2 || merged.SessionEntry.SpawnEntry[0] != "other-harness" {
		t.Errorf("SpawnEntry = %v, want overriding [other-harness {prompt}]", merged.SessionEntry.SpawnEntry)
	}
	if len(merged.SessionEntry.SpawnWindow) != 2 || merged.SessionEntry.SpawnWindow[0] != "sc-spawn-window" {
		t.Errorf("SpawnWindow = %v, want inherited", merged.SessionEntry.SpawnWindow)
	}
}

func TestSessionModelFlagsAccessorDefault(t *testing.T) {
	for _, sf := range []Sweatfile{
		{},
		{SessionEntry: &SessionEntry{}},
	} {
		got := sf.SessionModelFlags()
		want := map[string]string{"claude": "--model"}
		if len(got) != len(want) || got["claude"] != want["claude"] {
			t.Errorf("SessionModelFlags() = %v, want built-in default %v", got, want)
		}
	}
}

func TestSessionModelFlagsAccessorConfigured(t *testing.T) {
	sf := Sweatfile{
		SessionEntry: &SessionEntry{
			ModelFlags: map[string]string{"codex": "--model-name"},
		},
	}
	got := sf.SessionModelFlags()
	if len(got) != 1 || got["codex"] != "--model-name" {
		t.Errorf("SessionModelFlags() = %v, want configured map verbatim (no claude default folded in)", got)
	}
}

func TestMergeSessionModelFlagsPerKey(t *testing.T) {
	// Mirrors TestMergeSessionEnvPerKeyOverride: child adds a key without
	// dropping the parent's, and overrides a colliding key.
	base := Sweatfile{
		SessionEntry: &SessionEntry{
			ModelFlags: map[string]string{"claude": "--model", "circus": "--old-flag"},
		},
	}
	override := Sweatfile{
		SessionEntry: &SessionEntry{
			ModelFlags: map[string]string{"circus": "--new-flag", "codex": "--model-name"},
		},
	}
	merged := base.MergeWith(override)
	want := map[string]string{"claude": "--model", "circus": "--new-flag", "codex": "--model-name"}
	got := merged.SessionEntry.ModelFlags
	if len(got) != len(want) {
		t.Fatalf("merged ModelFlags = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("merged ModelFlags[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestMergeSessionModelFlagsInherit(t *testing.T) {
	base := Sweatfile{
		SessionEntry: &SessionEntry{ModelFlags: map[string]string{"claude": "--model"}},
	}
	override := Sweatfile{SessionEntry: &SessionEntry{Start: []string{"zellij"}}}
	merged := base.MergeWith(override)
	if got := merged.SessionEntry.ModelFlags; len(got) != 1 || got["claude"] != "--model" {
		t.Errorf("expected inherited ModelFlags, got %v", got)
	}
}

func TestParseSessionEntryEnvSubtable(t *testing.T) {
	input := `
[session-entry]
start = ["zmx", "-g", "spinclass", "attach", "$SPINCLASS_SESSION_ID"]

[session-entry.env]
SPINCLASS_GROUP = "spinclass"
FOO = "bar"
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	sf := doc.Data()
	if sf.SessionEntry == nil {
		t.Fatal("expected SessionEntry to be non-nil")
	}
	if got := sf.SessionEntry.Env["SPINCLASS_GROUP"]; got != "spinclass" {
		t.Errorf(`Env["SPINCLASS_GROUP"] = %q, want "spinclass"`, got)
	}
	if got := sf.SessionEntry.Env["FOO"]; got != "bar" {
		t.Errorf(`Env["FOO"] = %q, want "bar"`, got)
	}
	if got := sf.SessionEnv()["SPINCLASS_GROUP"]; got != "spinclass" {
		t.Errorf(`SessionEnv()["SPINCLASS_GROUP"] = %q, want "spinclass"`, got)
	}
}

func TestSessionEnvAccessorEmpty(t *testing.T) {
	if env := (Sweatfile{}).SessionEnv(); env != nil {
		t.Errorf("expected nil SessionEnv() when SessionEntry is unset, got %v", env)
	}
	sf := Sweatfile{SessionEntry: &SessionEntry{}}
	if env := sf.SessionEnv(); env != nil {
		t.Errorf("expected nil SessionEnv() when Env is unset, got %v", env)
	}
}

func TestMergeSessionEnvPerKey(t *testing.T) {
	// Parent sets two keys; child overrides one and adds one; child should
	// win on collision and the parent's untouched key must survive.
	base := Sweatfile{
		SessionEntry: &SessionEntry{
			Env: map[string]string{
				"SPINCLASS_GROUP": "parent",
				"FOO":             "from-parent",
			},
		},
	}
	override := Sweatfile{
		SessionEntry: &SessionEntry{
			Env: map[string]string{
				"SPINCLASS_GROUP": "child",
				"BAR":             "from-child",
			},
		},
	}
	merged := base.MergeWith(override)
	if merged.SessionEntry == nil {
		t.Fatal("expected SessionEntry to be non-nil after merge")
	}
	got := merged.SessionEntry.Env
	want := map[string]string{
		"SPINCLASS_GROUP": "child",
		"FOO":             "from-parent",
		"BAR":             "from-child",
	}
	if len(got) != len(want) {
		t.Fatalf("Env size = %d (got %v), want %d (%v)", len(got), got, len(want), want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Env[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestMergeSessionEnvInherit(t *testing.T) {
	base := Sweatfile{
		SessionEntry: &SessionEntry{
			Env: map[string]string{"SPINCLASS_GROUP": "parent"},
		},
	}
	override := Sweatfile{
		SessionEntry: &SessionEntry{Start: []string{"zellij"}},
	}
	merged := base.MergeWith(override)
	if merged.SessionEntry == nil || merged.SessionEntry.Env["SPINCLASS_GROUP"] != "parent" {
		t.Errorf("expected parent Env to be inherited, got %v", merged.SessionEntry)
	}
}

func TestParseStartCommands(t *testing.T) {
	input := `
[[start-commands]]
name = "jira"
description = "Start for a JIRA ticket"
arg-name = "ticket"
arg-help = "JIRA ticket ID"
arg-regex = "^[A-Z]+-[0-9]+$"
exec-completions = ["jira", "list"]
exec-start = ["jira", "show", "{arg}"]
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if len(sf.StartCommands) != 1 {
		t.Fatalf("expected 1 start-command, got %d", len(sf.StartCommands))
	}
	sc := sf.StartCommands[0]
	if sc.Name != "jira" {
		t.Errorf("Name = %q, want jira", sc.Name)
	}
	if sc.ArgName != "ticket" {
		t.Errorf("ArgName = %q, want ticket", sc.ArgName)
	}
	if sc.ArgRegex == nil || *sc.ArgRegex != "^[A-Z]+-[0-9]+$" {
		t.Errorf("ArgRegex = %v, want ^[A-Z]+-[0-9]+$", sc.ArgRegex)
	}
	if len(sc.ExecCompletions) != 2 || sc.ExecCompletions[0] != "jira" {
		t.Errorf("ExecCompletions = %v", sc.ExecCompletions)
	}
	if len(sc.ExecStart) != 3 || sc.ExecStart[2] != "{arg}" {
		t.Errorf("ExecStart = %v", sc.ExecStart)
	}
}

func TestParseStartCommandsMultiple(t *testing.T) {
	input := `
[[start-commands]]
name = "jira"
exec-start = ["echo", "jira"]

[[start-commands]]
name = "linear"
exec-start = ["echo", "linear"]
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if len(sf.StartCommands) != 2 {
		t.Fatalf("expected 2 start-commands, got %d", len(sf.StartCommands))
	}
	if sf.StartCommands[0].Name != "jira" || sf.StartCommands[1].Name != "linear" {
		t.Errorf("order = %q, %q", sf.StartCommands[0].Name, sf.StartCommands[1].Name)
	}
}

func TestMergeStartCommandsAppend(t *testing.T) {
	base := Sweatfile{
		StartCommands: []StartCommand{
			{Name: "jira", ExecStart: []string{"echo", "base"}},
		},
	}
	repo := Sweatfile{
		StartCommands: []StartCommand{
			{Name: "linear", ExecStart: []string{"echo", "linear"}},
		},
	}
	merged := base.MergeWith(repo)
	if len(merged.StartCommands) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(merged.StartCommands))
	}
	if merged.StartCommands[0].Name != "jira" || merged.StartCommands[1].Name != "linear" {
		t.Errorf("order broken: %v", merged.StartCommands)
	}
}

func TestMergeStartCommandsOverrideByName(t *testing.T) {
	base := Sweatfile{
		StartCommands: []StartCommand{
			{Name: "jira", ExecStart: []string{"echo", "base"}},
			{Name: "linear", ExecStart: []string{"echo", "linear"}},
		},
	}
	repo := Sweatfile{
		StartCommands: []StartCommand{
			{Name: "jira", ExecStart: []string{"echo", "override"}},
		},
	}
	merged := base.MergeWith(repo)
	if len(merged.StartCommands) != 2 {
		t.Fatalf("expected 2 entries after override, got %d", len(merged.StartCommands))
	}
	// Override keeps the slot position.
	if merged.StartCommands[0].Name != "jira" {
		t.Errorf("expected jira to stay at slot 0, got %v", merged.StartCommands)
	}
	if got := merged.StartCommands[0].ExecStart; len(got) != 2 || got[1] != "override" {
		t.Errorf("expected override prompt, got %v", got)
	}
}

func TestGetDefaultShipsGhIssueStartCommand(t *testing.T) {
	def := GetDefault()
	var found *StartCommand
	for i := range def.StartCommands {
		if def.StartCommands[i].Name == "gh_issue" {
			found = &def.StartCommands[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected gh_issue entry in GetDefault().StartCommands")
	}
	if found.ArgName != "issue" {
		t.Errorf("ArgName = %q, want issue", found.ArgName)
	}
	if found.ArgRegex == nil || *found.ArgRegex != "^[0-9]+$" {
		t.Errorf("ArgRegex = %v, want ^[0-9]+$", found.ArgRegex)
	}
	if len(found.ExecStart) == 0 {
		t.Error("ExecStart must be non-empty")
	}
	if len(found.ExecCompletions) == 0 {
		t.Error("ExecCompletions must be non-empty")
	}
}

func TestGetDefaultShipsGhPrStartCommand(t *testing.T) {
	def := GetDefault()
	var found *StartCommand
	for i := range def.StartCommands {
		if def.StartCommands[i].Name == "gh_pr" {
			found = &def.StartCommands[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected gh_pr entry in GetDefault().StartCommands")
	}
	if found.ArgName != "pr" {
		t.Errorf("ArgName = %q, want pr", found.ArgName)
	}
	if found.ArgRegex != nil {
		t.Errorf("ArgRegex should be nil (gh pr view handles validation), got %v", *found.ArgRegex)
	}
	if len(found.ExecStart) == 0 {
		t.Error("ExecStart must be non-empty")
	}
	if len(found.ExecCompletions) == 0 {
		t.Error("ExecCompletions must be non-empty")
	}
}

func TestMergePrecedenceUserOverridesDefault(t *testing.T) {
	userConfig := Sweatfile{
		StartCommands: []StartCommand{
			{
				Name:      "gh_issue",
				ExecStart: []string{"echo", "user override"},
			},
		},
	}
	merged := GetDefault().MergeWith(userConfig)
	var found *StartCommand
	for i := range merged.StartCommands {
		if merged.StartCommands[i].Name == "gh_issue" {
			found = &merged.StartCommands[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected gh_issue entry in merged result")
	}
	if len(found.ExecStart) != 2 || found.ExecStart[1] != "user override" {
		t.Errorf("user config should override default, got ExecStart = %v", found.ExecStart)
	}
}

func TestMergeWithDoesNotMutateBaseSlice(t *testing.T) {
	base := Sweatfile{
		StartCommands: []StartCommand{
			{Name: "a", ExecStart: []string{"echo", "a"}},
			{Name: "b", ExecStart: []string{"echo", "b"}},
		},
	}
	origLen := len(base.StartCommands)
	origFirst := base.StartCommands[0].ExecStart[1]

	other := Sweatfile{
		StartCommands: []StartCommand{
			{Name: "a", ExecStart: []string{"echo", "overridden"}},
			{Name: "c", ExecStart: []string{"echo", "c"}},
		},
	}
	merged := base.MergeWith(other)

	// base should be untouched
	if len(base.StartCommands) != origLen {
		t.Errorf("base.StartCommands length changed from %d to %d", origLen, len(base.StartCommands))
	}
	if base.StartCommands[0].ExecStart[1] != origFirst {
		t.Errorf("base.StartCommands[0] mutated: ExecStart[1] = %q, want %q",
			base.StartCommands[0].ExecStart[1], origFirst)
	}

	// merged should have 3 entries: a (overridden), b, c
	if len(merged.StartCommands) != 3 {
		t.Fatalf("expected 3 merged entries, got %d", len(merged.StartCommands))
	}
	if merged.StartCommands[0].ExecStart[1] != "overridden" {
		t.Errorf("merged[0] should be overridden, got %q", merged.StartCommands[0].ExecStart[1])
	}
}

func TestParseHooksDisableMerge(t *testing.T) {
	input := `
[hooks]
disable-merge = true
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if !sf.DisableMergeEnabled() {
		t.Error("expected disable-merge to be enabled")
	}
}

func TestParseHooksDisableMergeAbsent(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.DisableMergeEnabled() {
		t.Error("expected disable-merge to be disabled when absent")
	}
}

func TestMergeDisableMergeInherit(t *testing.T) {
	enabled := true
	base := Sweatfile{Hooks: &Hooks{DisableMerge: &enabled}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if !merged.DisableMergeEnabled() {
		t.Error("expected inherited disable-merge")
	}
}

func TestMergeDisableMergeOverride(t *testing.T) {
	enabled := true
	disabled := false
	base := Sweatfile{Hooks: &Hooks{DisableMerge: &enabled}}
	repo := Sweatfile{Hooks: &Hooks{DisableMerge: &disabled}}
	merged := base.MergeWith(repo)
	if merged.DisableMergeEnabled() {
		t.Error("expected overridden disable-merge to be disabled")
	}
}

func TestParseHooksDisableNixGC(t *testing.T) {
	input := `
[hooks]
disable-nix-gc = true
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if !sf.DisableNixGCEnabled() {
		t.Error("expected disable-nix-gc to be enabled")
	}
}

func TestParseHooksDisableNixGCAbsent(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.DisableNixGCEnabled() {
		t.Error("expected disable-nix-gc to be disabled when absent")
	}
}

func TestMergeDisableNixGCInherit(t *testing.T) {
	enabled := true
	base := Sweatfile{Hooks: &Hooks{DisableNixGC: &enabled}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if !merged.DisableNixGCEnabled() {
		t.Error("expected inherited disable-nix-gc")
	}
}

func TestMergeDisableNixGCOverride(t *testing.T) {
	enabled := true
	disabled := false
	base := Sweatfile{Hooks: &Hooks{DisableNixGC: &enabled}}
	repo := Sweatfile{Hooks: &Hooks{DisableNixGC: &disabled}}
	merged := base.MergeWith(repo)
	if merged.DisableNixGCEnabled() {
		t.Error("expected overridden disable-nix-gc to be disabled")
	}
}

func TestParseHooksDisableImplicitSessions(t *testing.T) {
	input := `
[hooks]
disable-implicit-sessions = true
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if !sf.DisableImplicitSessionsEnabled() {
		t.Error("expected disable-implicit-sessions to be enabled")
	}
	// The regenerated decoder must CONSUME the key (no undecoded leftovers),
	// else `sc validate` would flag it as unknown.
	if u := doc.Undecoded(); len(u) != 0 {
		t.Errorf("expected no undecoded keys, got %v", u)
	}
}

func TestParseHooksDisableImplicitSessionsAbsent(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.DisableImplicitSessionsEnabled() {
		t.Error("expected disable-implicit-sessions to be disabled when absent")
	}
}

func TestMergeDisableImplicitSessionsInherit(t *testing.T) {
	enabled := true
	base := Sweatfile{Hooks: &Hooks{DisableImplicitSessions: &enabled}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if !merged.DisableImplicitSessionsEnabled() {
		t.Error("expected inherited disable-implicit-sessions")
	}
}

func TestMergeDisableImplicitSessionsOverride(t *testing.T) {
	enabled := true
	disabled := false
	base := Sweatfile{Hooks: &Hooks{DisableImplicitSessions: &enabled}}
	repo := Sweatfile{Hooks: &Hooks{DisableImplicitSessions: &disabled}}
	merged := base.MergeWith(repo)
	if merged.DisableImplicitSessionsEnabled() {
		t.Error("expected overridden disable-implicit-sessions to be disabled")
	}
}

func TestParseHooksDisableMergeBuildWorktree(t *testing.T) {
	input := `
[hooks]
disable-merge-build-worktree = true
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if !sf.MergeBuildWorktreeDisabled() {
		t.Error("expected disable-merge-build-worktree to be enabled")
	}
}

func TestParseHooksDisableMergeBuildWorktreeAbsent(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.MergeBuildWorktreeDisabled() {
		t.Error("expected disable-merge-build-worktree to default to false when absent")
	}
}

func TestMergeDisableMergeBuildWorktreeInherit(t *testing.T) {
	enabled := true
	base := Sweatfile{Hooks: &Hooks{DisableMergeBuildWorktree: &enabled}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if !merged.MergeBuildWorktreeDisabled() {
		t.Error("expected inherited disable-merge-build-worktree")
	}
}

func TestMergeDisableMergeBuildWorktreeOverride(t *testing.T) {
	enabled := true
	disabled := false
	base := Sweatfile{Hooks: &Hooks{DisableMergeBuildWorktree: &enabled}}
	repo := Sweatfile{Hooks: &Hooks{DisableMergeBuildWorktree: &disabled}}
	merged := base.MergeWith(repo)
	if merged.MergeBuildWorktreeDisabled() {
		t.Error("expected overridden disable-merge-build-worktree to be disabled")
	}
}

func TestParseHooksDisableWorktreePathRewrite(t *testing.T) {
	input := `
[hooks]
disable-worktree-path-rewrite = true
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if !sf.WorktreePathRewriteDisabled() {
		t.Error("expected disable-worktree-path-rewrite to be enabled")
	}
}

func TestParseHooksDisableWorktreePathRewriteAbsent(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.WorktreePathRewriteDisabled() {
		t.Error("expected disable-worktree-path-rewrite to default to false when absent")
	}
}

func TestMergeDisableWorktreePathRewriteInherit(t *testing.T) {
	enabled := true
	base := Sweatfile{Hooks: &Hooks{DisableWorktreePathRewrite: &enabled}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if !merged.WorktreePathRewriteDisabled() {
		t.Error("expected inherited disable-worktree-path-rewrite")
	}
}

func TestMergeDisableWorktreePathRewriteOverride(t *testing.T) {
	enabled := true
	disabled := false
	base := Sweatfile{Hooks: &Hooks{DisableWorktreePathRewrite: &enabled}}
	repo := Sweatfile{Hooks: &Hooks{DisableWorktreePathRewrite: &disabled}}
	merged := base.MergeWith(repo)
	if merged.WorktreePathRewriteDisabled() {
		t.Error("expected overridden disable-worktree-path-rewrite to be disabled")
	}
}
