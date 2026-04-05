package sweatfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMinimal(t *testing.T) {
	input := `
[git]
excludes = [".claude/"]
`
	doc, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Git == nil || len(sf.Git.Excludes) != 1 || sf.Git.Excludes[0] != ".claude/" {
		t.Errorf("git.excludes: got %v", sf.Git)
	}
}

func TestParseEmpty(t *testing.T) {
	doc, err := Parse([]byte(""))
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
	os.WriteFile(path, []byte("[git]\nexcludes = [\".direnv/\"]"), 0o644)

	doc, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Git == nil || len(sf.Git.Excludes) != 1 || sf.Git.Excludes[0] != ".direnv/" {
		t.Errorf("git.excludes: got %v", sf.Git)
	}
}

func TestLoadMissing(t *testing.T) {
	doc, err := Load("/nonexistent/sweatfile")
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
	doc, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = doc.Save(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loaded, err := Load(path)
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
	doc, err := Parse([]byte(input))
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

	result, err := LoadHierarchy(home, repoDir)
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

	result, err := LoadHierarchy(home, repoDir)
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

	result, err := LoadHierarchy(home, repoDir)
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

	result, err := LoadHierarchy(home, repoDir)
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

func TestParseHooksCreate(t *testing.T) {
	input := `
[hooks]
create = "composer install"
`
	doc, err := Parse([]byte(input))
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
	doc, err := Parse([]byte(input))
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
	doc, err := Parse([]byte(input))
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
	doc, err := Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
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
	doc, err := Parse([]byte(input))
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

	result, err := LoadHierarchy(home, repoDir)
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
	os.MkdirAll(repoDir, 0o755)

	globalPath := filepath.Join(home, ".config", "spinclass", "sweatfile")
	writeSweatfile(t, globalPath, "[hooks]\nstop = \"just test\"")

	result, err := LoadHierarchy(home, repoDir)
	if err != nil {
		t.Fatalf("LoadHierarchy returned error: %v", err)
	}

	if result.Merged.StopHookCommand() == nil ||
		*result.Merged.StopHookCommand() != "just test" {
		t.Errorf("expected inherited hooks.stop, got %v", result.Merged.Hooks)
	}
}

func TestParseSystemPrompt(t *testing.T) {
	input := "[claude]\nsystem-prompt = \"do stuff\""
	doc, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Claude == nil || sf.Claude.SystemPrompt == nil || *sf.Claude.SystemPrompt != "do stuff" {
		t.Errorf("claude.system-prompt: got %v", sf.Claude)
	}
}

func TestParseSystemPromptEmpty(t *testing.T) {
	input := "[claude]\nsystem-prompt = \"\""
	doc, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Claude == nil || sf.Claude.SystemPrompt == nil {
		t.Fatal("expected non-nil claude.system-prompt for explicit empty string")
	}
	if *sf.Claude.SystemPrompt != "" {
		t.Errorf("expected empty system-prompt, got %q", *sf.Claude.SystemPrompt)
	}
}

func TestParseSystemPromptAbsent(t *testing.T) {
	doc, err := Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Claude != nil {
		t.Errorf("expected nil claude, got %v", sf.Claude)
	}
}

func TestParseSystemPromptAppend(t *testing.T) {
	input := "[claude]\nsystem-prompt-append = \"extra instructions\""
	doc, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Claude == nil || sf.Claude.SystemPromptAppend == nil ||
		*sf.Claude.SystemPromptAppend != "extra instructions" {
		t.Errorf("claude.system-prompt-append: got %v", sf.Claude)
	}
}

func TestParseSystemPromptAppendAbsent(t *testing.T) {
	doc, err := Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.Claude != nil {
		t.Errorf(
			"expected nil claude, got %v",
			sf.Claude,
		)
	}
}

func TestMergeSystemPromptInherit(t *testing.T) {
	prompt := "base prompt"
	base := Sweatfile{Claude: &Claude{SystemPrompt: &prompt}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if merged.Claude == nil || merged.Claude.SystemPrompt == nil || *merged.Claude.SystemPrompt != "base prompt" {
		t.Errorf(
			"expected inherited system-prompt, got %v",
			merged.Claude,
		)
	}
}

func TestMergeSystemPromptConcatenate(t *testing.T) {
	basePrompt := "base prompt"
	repoPrompt := "repo prompt"
	base := Sweatfile{Claude: &Claude{SystemPrompt: &basePrompt}}
	repo := Sweatfile{Claude: &Claude{SystemPrompt: &repoPrompt}}
	merged := base.MergeWith(repo)
	if merged.Claude == nil || merged.Claude.SystemPrompt == nil ||
		*merged.Claude.SystemPrompt != "base prompt repo prompt" {
		t.Errorf(
			"expected concatenated system-prompt, got %v",
			merged.Claude,
		)
	}
}

func TestMergeSystemPromptClear(t *testing.T) {
	basePrompt := "base prompt"
	empty := ""
	base := Sweatfile{Claude: &Claude{SystemPrompt: &basePrompt}}
	repo := Sweatfile{Claude: &Claude{SystemPrompt: &empty}}
	merged := base.MergeWith(repo)
	if merged.Claude == nil || merged.Claude.SystemPrompt == nil {
		t.Fatal("expected non-nil system-prompt after clear")
	}
	if *merged.Claude.SystemPrompt != "" {
		t.Errorf("expected cleared system-prompt, got %q", *merged.Claude.SystemPrompt)
	}
}

func TestMergeSystemPromptAppendInherit(t *testing.T) {
	prompt := "base append"
	base := Sweatfile{Claude: &Claude{SystemPromptAppend: &prompt}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if merged.Claude == nil || merged.Claude.SystemPromptAppend == nil ||
		*merged.Claude.SystemPromptAppend != "base append" {
		t.Errorf(
			"expected inherited system-prompt-append, got %v",
			merged.Claude,
		)
	}
}

func TestMergeSystemPromptAppendConcatenate(t *testing.T) {
	basePrompt := "base append"
	repoPrompt := "repo append"
	base := Sweatfile{Claude: &Claude{SystemPromptAppend: &basePrompt}}
	repo := Sweatfile{Claude: &Claude{SystemPromptAppend: &repoPrompt}}
	merged := base.MergeWith(repo)
	if merged.Claude == nil || merged.Claude.SystemPromptAppend == nil ||
		*merged.Claude.SystemPromptAppend != "base append repo append" {
		t.Errorf(
			"expected concatenated system-prompt-append, got %v",
			merged.Claude,
		)
	}
}

func TestMergeSystemPromptAppendClear(t *testing.T) {
	basePrompt := "base append"
	empty := ""
	base := Sweatfile{Claude: &Claude{SystemPromptAppend: &basePrompt}}
	repo := Sweatfile{Claude: &Claude{SystemPromptAppend: &empty}}
	merged := base.MergeWith(repo)
	if merged.Claude == nil || merged.Claude.SystemPromptAppend == nil {
		t.Fatal("expected non-nil system-prompt-append after clear")
	}
	if *merged.Claude.SystemPromptAppend != "" {
		t.Errorf(
			"expected cleared system-prompt-append, got %q",
			*merged.Claude.SystemPromptAppend,
		)
	}
}

func TestParseEnvrcDirectives(t *testing.T) {
	input := "[direnv]\nenvrc = [\"source_up\", \"dotenv_if_exists\"]"
	doc, err := Parse([]byte(input))
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
	doc, err := Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
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
	doc, err := Parse([]byte(input))
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
	doc, err := Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
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
	os.MkdirAll(repoDir, 0o755)

	globalPath := filepath.Join(home, ".config", "spinclass", "sweatfile")
	writeSweatfile(t, globalPath, "[hooks]\nstop = \"just test\"")

	repoSweatfile := filepath.Join(repoDir, "sweatfile")
	writeSweatfile(t, repoSweatfile, "[hooks]\nstop = \"just lint\"")

	result, err := LoadHierarchy(home, repoDir)
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
	doc, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if !sf.DisallowMainWorktreeEnabled() {
		t.Error("expected disallow-main-worktree to be enabled")
	}
}

func TestParseHooksDisallowMainWorktreeAbsent(t *testing.T) {
	doc, err := Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
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
	os.MkdirAll(worktreeDir, 0o755)

	// Main repo sweatfile enables disallow-main-worktree
	writeSweatfile(t, filepath.Join(mainRepo, "sweatfile"),
		"[hooks]\ndisallow-main-worktree = true\n")

	result, err := LoadWorktreeHierarchy(home, mainRepo, worktreeDir)
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
	os.MkdirAll(worktreeDir, 0o755)

	// Main repo enables it
	writeSweatfile(t, filepath.Join(mainRepo, "sweatfile"),
		"[hooks]\ndisallow-main-worktree = true\n")

	// Worktree disables it
	writeSweatfile(t, filepath.Join(worktreeDir, "sweatfile"),
		"[hooks]\ndisallow-main-worktree = false\n")

	result, err := LoadWorktreeHierarchy(home, mainRepo, worktreeDir)
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
	doc, err := Parse([]byte("[hooks]\ntool-use-log = true\n"))
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

func TestResolvePathOrStringLiteral(t *testing.T) {
	result := resolvePathOrString("just a string")
	if result != "just a string" {
		t.Errorf("expected literal string, got %q", result)
	}
}

func TestResolvePathOrStringFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.txt")
	os.WriteFile(path, []byte("contents from file\n"), 0o644)

	result := resolvePathOrString(path)
	if result != "contents from file" {
		t.Errorf("expected file contents, got %q", result)
	}
}

func TestResolvePathOrStringEnvVar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.txt")
	os.WriteFile(path, []byte("env resolved\n"), 0o644)

	t.Setenv("TEST_RESOLVE_DIR", dir)
	result := resolvePathOrString("$TEST_RESOLVE_DIR/prompt.txt")
	if result != "env resolved" {
		t.Errorf("expected env-expanded file contents, got %q", result)
	}
}

func TestResolvePathOrStringNonexistentFile(t *testing.T) {
	result := resolvePathOrString("/nonexistent/path/to/file.txt")
	if result != "/nonexistent/path/to/file.txt" {
		t.Errorf("expected literal fallback, got %q", result)
	}
}

func TestRoundTripPreservesComments(t *testing.T) {
	input := `# Global config

[git]
excludes = [".claude/", ".direnv/"]

[claude]
system-prompt = "be helpful"
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
	doc, err := Parse([]byte(input))
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
	doc, err := Parse([]byte(input))
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
	doc, err := Parse([]byte(""))
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
