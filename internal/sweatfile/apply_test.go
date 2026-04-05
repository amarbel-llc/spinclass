package sweatfile

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitDir returns the directory containing the git binary, for use in tests
// that override PATH but still need git available.
func gitDir(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not in PATH")
	}
	return filepath.Dir(gitPath)
}

func TestPrepareLocalBinWorksInsideWorktree(t *testing.T) {
	// Proves #65: prepareLocalBin uses relative ".git/spinclass/bin/" which
	// fails inside a worktree where .git is a file, not a directory.
	repoDir := t.TempDir()

	// Create a real git repo + worktree so git rev-parse works
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s (%v)", args, out, err)
		}
	}

	run(repoDir, "init")
	os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("x"), 0o644)
	run(repoDir, "add", "f.txt")
	run(repoDir, "commit", "-m", "init")

	wtDir := filepath.Join(repoDir, ".worktrees", "test-wt")
	os.MkdirAll(filepath.Dir(wtDir), 0o755)
	run(repoDir, "worktree", "add", "-b", "test-wt", wtDir)

	// Verify .git in worktree is a file (precondition)
	info, err := os.Lstat(filepath.Join(wtDir, ".git"))
	if err != nil {
		t.Fatalf("stat .git: %v", err)
	}
	if info.IsDir() {
		t.Fatal("expected .git to be a file in worktree, got directory")
	}

	// cd into the worktree — this is the scenario that triggers #65
	origDir, _ := os.Getwd()
	if err := os.Chdir(wtDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })

	sf := Sweatfile{}
	err = sf.prepareLocalBin()
	if err != nil {
		t.Fatalf("prepareLocalBin failed inside worktree: %v", err)
	}

	// The bin dir should have been created inside the main repo's .git,
	// not as a relative ".git/spinclass/bin" from the worktree
	binPath := filepath.Join(repoDir, ".git", "spinclass", "bin")
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		t.Errorf("expected bin dir at %s", binPath)
	}
}

func TestHardcodedDefaultsGitExcludes(t *testing.T) {
	defaults := GetDefault()

	if defaults.Git == nil {
		t.Fatal("expected non-nil Git struct")
	}

	if defaults.Git.Excludes == nil {
		t.Fatal("expected non-nil git excludes slice")
	}

	if len(defaults.Git.Excludes) != 3 {
		t.Fatalf(
			"expected 3 git excludes, got %d: %v",
			len(defaults.Git.Excludes),
			defaults.Git.Excludes,
		)
	}

	if defaults.Git.Excludes[0] != ".worktrees/" {
		t.Errorf("expected .worktrees/, got %q", defaults.Git.Excludes[0])
	}

	if defaults.Git.Excludes[1] != ".spinclass/" {
		t.Errorf("expected .spinclass/, got %q", defaults.Git.Excludes[1])
	}

	if defaults.Git.Excludes[2] != ".mcp.json" {
		t.Errorf("expected .mcp.json, got %q", defaults.Git.Excludes[2])
	}
}

func TestHardcodedDefaultsClaudeAllow(t *testing.T) {
	defaults := GetDefault()

	home, _ := os.UserHomeDir()
	if home == "" {
		if defaults.Claude != nil {
			t.Errorf(
				"expected nil Claude when HOME is empty, got %v",
				defaults.Claude,
			)
		}
		return
	}

	if defaults.Claude == nil {
		t.Fatal("expected non-nil Claude struct")
	}

	if len(defaults.Claude.Allow) != 1 {
		t.Fatalf(
			"expected 1 claude allow rule, got %d: %v",
			len(defaults.Claude.Allow),
			defaults.Claude.Allow,
		)
	}

	wantRule := "Read(" + filepath.Join(home, ".claude") + "/*)"
	if defaults.Claude.Allow[0] != wantRule {
		t.Errorf(
			"Claude.Allow[0]: got %q, want %q",
			defaults.Claude.Allow[0],
			wantRule,
		)
	}
}

func TestApplyClaudeSettings(t *testing.T) {
	dir := t.TempDir()
	rules := []string{"Read", "Glob", "Bash(git *)"}

	err := ApplyClaudeSettings(dir, Sweatfile{Claude: &Claude{Allow: rules}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(
		filepath.Join(dir, ".claude", "settings.local.json"),
	)
	if err != nil {
		t.Fatalf("reading settings: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing settings: %v", err)
	}

	permsMap, _ := doc["permissions"].(map[string]any)
	if permsMap == nil {
		t.Fatal("expected permissions key")
	}

	defaultMode, _ := permsMap["defaultMode"].(string)
	if defaultMode != "acceptEdits" {
		t.Errorf("defaultMode: got %q, want %q", defaultMode, "acceptEdits")
	}

	allowRaw, _ := permsMap["allow"].([]any)
	if len(allowRaw) != 6 {
		t.Fatalf(
			"expected 6 rules (3 passed + 3 scoped), got %d: %v",
			len(allowRaw),
			allowRaw,
		)
	}

	// First 3 are from the passed rules
	for i, want := range rules {
		got, _ := allowRaw[i].(string)
		if got != want {
			t.Errorf("rule %d: got %q, want %q", i, got, want)
		}
	}

	// Last 3 are auto-injected scoped rules
	readRule, _ := allowRaw[3].(string)
	editRule, _ := allowRaw[4].(string)
	writeRule, _ := allowRaw[5].(string)

	wantRead := "Read(" + dir + "/*)"
	wantEdit := "Edit(" + dir + "/*)"
	wantWrite := "Write(" + dir + "/*)"
	if readRule != wantRead {
		t.Errorf("read rule: got %q, want %q", readRule, wantRead)
	}
	if editRule != wantEdit {
		t.Errorf("edit rule: got %q, want %q", editRule, wantEdit)
	}
	if writeRule != wantWrite {
		t.Errorf("write rule: got %q, want %q", writeRule, wantWrite)
	}
}

func TestApplyClaudeSettingsEmpty(t *testing.T) {
	dir := t.TempDir()

	err := ApplyClaudeSettings(dir, Sweatfile{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(
		filepath.Join(dir, ".claude", "settings.local.json"),
	)
	if err != nil {
		t.Fatalf("reading settings: %v", err)
	}

	var doc map[string]any
	json.Unmarshal(data, &doc)
	permsMap, _ := doc["permissions"].(map[string]any)
	allowRaw, _ := permsMap["allow"].([]any)

	// Even with no passed rules, the 3 scoped rules are injected
	if len(allowRaw) != 3 {
		t.Fatalf("expected 3 scoped rules, got %d: %v", len(allowRaw), allowRaw)
	}
}

func TestApplyClaudeSettingsOverwritesExistingKeys(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	os.MkdirAll(claudeDir, 0o755)

	existing := map[string]any{
		"mcpServers": map[string]any{"test": true},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), data, 0o644)

	err := ApplyClaudeSettings(dir, Sweatfile{Claude: &Claude{Allow: []string{"Read"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, _ := os.ReadFile(filepath.Join(claudeDir, "settings.local.json"))
	var doc map[string]any
	json.Unmarshal(result, &doc)

	if _, ok := doc["mcpServers"]; ok {
		t.Error("expected mcpServers key to be overwritten")
	}
}

func TestApplyClaudeSettingsWritesHooksForWorktree(t *testing.T) {
	dir := t.TempDir()

	// Simulate a worktree by creating .git as a file (not directory)
	os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /tmp/fake"), 0o644)

	err := ApplyClaudeSettings(dir, Sweatfile{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(
		filepath.Join(dir, ".claude", "settings.local.json"),
	)
	if err != nil {
		t.Fatalf("reading settings: %v", err)
	}

	var doc map[string]any
	json.Unmarshal(data, &doc)

	hooksRaw, ok := doc["hooks"]
	if !ok {
		t.Fatal("expected hooks key in settings")
	}

	hooks := hooksRaw.(map[string]any)
	preToolUse, ok := hooks["PreToolUse"]
	if !ok {
		t.Fatal("expected PreToolUse key in hooks")
	}

	entries := preToolUse.([]any)
	if len(entries) != 1 {
		t.Fatalf("expected 1 PreToolUse entry, got %d", len(entries))
	}

	entry := entries[0].(map[string]any)
	matcher := entry["matcher"].(string)
	if matcher != "*" {
		t.Errorf("matcher: got %q", matcher)
	}

	hooksList := entry["hooks"].([]any)
	hook := hooksList[0].(map[string]any)
	if hook["type"] != "command" {
		t.Errorf("hook type: got %q", hook["type"])
	}
	hookCmd := hook["command"].(string)
	if !strings.HasSuffix(hookCmd, " hooks") {
		t.Errorf("hook command: got %q", hook["command"])
	}
}

func TestApplyClaudeSettingsNoHooksForMainRepo(t *testing.T) {
	dir := t.TempDir()

	// Simulate a main repo by creating .git as a directory
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)

	err := ApplyClaudeSettings(dir, Sweatfile{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	var doc map[string]any
	json.Unmarshal(data, &doc)

	if _, ok := doc["hooks"]; ok {
		t.Error("expected no hooks key for main repo")
	}
}

func TestApplyClaudeSettingsWritesStopHookWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /tmp/fake"), 0o644)

	cmd := "just test"
	err := ApplyClaudeSettings(dir, Sweatfile{Hooks: &Hooks{Stop: &cmd}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	var doc map[string]any
	json.Unmarshal(data, &doc)

	hooks := doc["hooks"].(map[string]any)

	stopRaw, ok := hooks["Stop"]
	if !ok {
		t.Fatal("expected Stop key in hooks")
	}

	entries := stopRaw.([]any)
	if len(entries) != 1 {
		t.Fatalf("expected 1 Stop entry, got %d", len(entries))
	}

	entry := entries[0].(map[string]any)
	if entry["matcher"] != "*" {
		t.Errorf("matcher: got %q", entry["matcher"])
	}
}

func TestApplyClaudeSettingsNoStopHookWhenNotConfigured(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /tmp/fake"), 0o644)

	err := ApplyClaudeSettings(dir, Sweatfile{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	var doc map[string]any
	json.Unmarshal(data, &doc)

	hooks := doc["hooks"].(map[string]any)
	if _, ok := hooks["Stop"]; ok {
		t.Error("expected no Stop key when stop-hook is not configured")
	}
}

func TestPrepareDirenvWritesEnvrcWithoutUseFlakeWhenNoFlakeNix(t *testing.T) {
	dir := t.TempDir()

	// Create a fake direnv that just exits 0
	fakeBin := t.TempDir()
	fakeDirenv := filepath.Join(fakeBin, "direnv")
	os.WriteFile(fakeDirenv, []byte("#!/bin/sh\nexit 0\n"), 0o755)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+":"+gitDir(t))
	defer os.Setenv("PATH", origPath)

	err := Sweatfile{}.prepareDirenv(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".envrc"))
	if err != nil {
		t.Fatalf("reading .envrc: %v", err)
	}

	content := string(data)

	// Should have source_up and PATH_add but NOT use flake
	if !strings.Contains(content, "source_up\n") {
		t.Errorf("expected source_up directive, got %q", content)
	}
	if strings.Contains(content, "use flake") {
		t.Errorf("expected no use flake directive, got %q", content)
	}
	if !strings.Contains(content, "PATH_add") || !strings.Contains(content, "spinclass/bin") {
		t.Errorf("expected PATH_add with spinclass/bin, got %q", content)
	}
}

func TestPrepareDirenvSkipsWhenDirenvNotInPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("{}"), 0o644)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	defer os.Setenv("PATH", origPath)

	err := Sweatfile{}.prepareDirenv(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	envrcPath := filepath.Join(dir, ".envrc")
	if _, err := os.Stat(envrcPath); err == nil {
		t.Error("expected no .envrc when direnv is not in PATH")
	}
}

func TestPrepareDirenvWritesEnvrc(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("{}"), 0o644)

	// Create a fake direnv that just exits 0
	fakeBin := t.TempDir()
	fakeDirenv := filepath.Join(fakeBin, "direnv")
	os.WriteFile(fakeDirenv, []byte("#!/bin/sh\nexit 0\n"), 0o755)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+":"+gitDir(t))
	defer os.Setenv("PATH", origPath)

	err := Sweatfile{}.prepareDirenv(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".envrc"))
	if err != nil {
		t.Fatalf("reading .envrc: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "source_up\n") {
		t.Errorf("expected source_up, got %q", content)
	}
	if !strings.Contains(content, "use flake\n") {
		t.Errorf("expected use flake, got %q", content)
	}
	if !strings.Contains(content, "PATH_add") || !strings.Contains(content, "spinclass/bin") {
		t.Errorf("expected PATH_add with spinclass/bin, got %q", content)
	}
}

func TestPrepareDirenvOverwritesExistingEnvrc(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(dir, ".envrc"), []byte("old content\n"), 0o644)

	fakeBin := t.TempDir()
	fakeDirenv := filepath.Join(fakeBin, "direnv")
	os.WriteFile(fakeDirenv, []byte("#!/bin/sh\nexit 0\n"), 0o755)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+":"+gitDir(t))
	defer os.Setenv("PATH", origPath)

	err := Sweatfile{}.prepareDirenv(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".envrc"))
	if err != nil {
		t.Fatalf("reading .envrc: %v", err)
	}

	content := string(data)
	if strings.Contains(content, "old content") {
		t.Errorf("expected old content to be replaced, got %q", content)
	}
	if !strings.Contains(content, "source_up\n") || !strings.Contains(content, "use flake\n") {
		t.Errorf("expected source_up and use flake, got %q", content)
	}
	if !strings.Contains(content, "PATH_add") || !strings.Contains(content, "spinclass/bin") {
		t.Errorf("expected PATH_add with spinclass/bin, got %q", content)
	}
}

func TestWriteEnvrcWithDirectives(t *testing.T) {
	dir := t.TempDir()

	fakeBin := t.TempDir()
	os.WriteFile(
		filepath.Join(fakeBin, "direnv"),
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	)
	t.Setenv("PATH", fakeBin+":"+gitDir(t))

	sf := Sweatfile{Direnv: &Direnv{Envrc: []string{"source_up", "dotenv_if_exists"}}}
	err := sf.prepareDirenv(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".envrc"))
	content := string(data)

	if !strings.Contains(content, "source_up\n") {
		t.Errorf("expected source_up directive, got %q", content)
	}
	if !strings.Contains(content, "dotenv_if_exists\n") {
		t.Errorf("expected dotenv_if_exists directive, got %q", content)
	}
	if !strings.Contains(content, "PATH_add") || !strings.Contains(content, "spinclass/bin") {
		t.Errorf("expected PATH_add with spinclass/bin, got %q", content)
	}
}

func TestWriteEnvrcDefaultFallbackWithFlake(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("{}"), 0o644)

	fakeBin := t.TempDir()
	os.WriteFile(
		filepath.Join(fakeBin, "direnv"),
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	)
	t.Setenv("PATH", fakeBin+":"+gitDir(t))

	sf := Sweatfile{}
	err := sf.prepareDirenv(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".envrc"))
	content := string(data)

	if !strings.Contains(content, "source_up\n") || !strings.Contains(content, "use flake\n") {
		t.Errorf("expected source_up and use flake, got %q", content)
	}
	if !strings.Contains(content, "PATH_add") || !strings.Contains(content, "spinclass/bin") {
		t.Errorf("expected PATH_add with spinclass/bin, got %q", content)
	}
}

func TestWriteEnvrcDefaultFallbackWithoutFlake(t *testing.T) {
	dir := t.TempDir()

	fakeBin := t.TempDir()
	os.WriteFile(
		filepath.Join(fakeBin, "direnv"),
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	)
	t.Setenv("PATH", fakeBin+":"+gitDir(t))

	sf := Sweatfile{}
	err := sf.prepareDirenv(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".envrc"))
	content := string(data)

	if !strings.Contains(content, "source_up\n") {
		t.Errorf("expected source_up, got %q", content)
	}
	if strings.Contains(content, "use flake") {
		t.Errorf("expected no use flake, got %q", content)
	}
	if !strings.Contains(content, "PATH_add") || !strings.Contains(content, "spinclass/bin") {
		t.Errorf("expected PATH_add with spinclass/bin, got %q", content)
	}
}

func TestWriteSpinclassEnv(t *testing.T) {
	dir := t.TempDir()

	fakeBin := t.TempDir()
	os.WriteFile(
		filepath.Join(fakeBin, "direnv"),
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	)
	t.Setenv("PATH", fakeBin+":"+gitDir(t))

	sf := Sweatfile{
		Direnv: &Direnv{
			Dotenv: map[string]string{
				"FOO": "bar",
				"BAZ": "qux",
			},
		},
	}
	err := sf.Apply(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".spinclass.env"))
	if err != nil {
		t.Fatalf("reading .spinclass.env: %v", err)
	}

	content := string(data)
	if content != "BAZ=qux\nFOO=bar\n" {
		t.Errorf(".spinclass.env content: got %q", content)
	}
}

func TestWriteSpinclassEnvInterpolatesWorktree(t *testing.T) {
	dir := t.TempDir()

	fakeBin := t.TempDir()
	os.WriteFile(
		filepath.Join(fakeBin, "direnv"),
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	)
	t.Setenv("PATH", fakeBin+":"+gitDir(t))

	sf := Sweatfile{
		Direnv: &Direnv{
			Dotenv: map[string]string{
				"INCLUDE_PATH": "$WORKTREE/lib:.",
			},
		},
	}
	err := sf.Apply(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".spinclass.env"))
	want := fmt.Sprintf("INCLUDE_PATH=%s/lib:.\n", dir)
	if string(data) != want {
		t.Errorf(
			".spinclass.env content:\ngot  %q\nwant %q",
			string(data),
			want,
		)
	}
}

func TestEnvAutoDotenvDirective(t *testing.T) {
	dir := t.TempDir()

	fakeBin := t.TempDir()
	os.WriteFile(
		filepath.Join(fakeBin, "direnv"),
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	)
	t.Setenv("PATH", fakeBin+":"+gitDir(t))

	sf := Sweatfile{
		Direnv: &Direnv{
			Dotenv: map[string]string{"FOO": "bar"},
		},
	}
	err := sf.Apply(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".envrc"))
	content := string(data)
	if !strings.Contains(content, "dotenv .spinclass.env") {
		t.Errorf("expected dotenv .spinclass.env in .envrc, got %q", content)
	}
}

func TestNoEnvNoDotenvDirective(t *testing.T) {
	dir := t.TempDir()

	fakeBin := t.TempDir()
	os.WriteFile(
		filepath.Join(fakeBin, "direnv"),
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	)
	t.Setenv("PATH", fakeBin+":"+gitDir(t))

	sf := Sweatfile{}
	err := sf.Apply(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".envrc"))
	content := string(data)
	if strings.Contains(content, "dotenv") {
		t.Errorf(
			"expected no dotenv in .envrc when env is empty, got %q",
			content,
		)
	}
}

func TestRunCreateHookExecutes(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "hook-ran")

	cmd := fmt.Sprintf("touch %s", marker)
	sf := Sweatfile{Hooks: &Hooks{Create: &cmd}}

	err := sf.RunCreateHook(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("expected create hook to run and create marker file")
	}
}

func TestRunCreateHookReceivesWorktreeEnv(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "worktree-path")

	cmd := fmt.Sprintf("echo $WORKTREE > %s", output)
	sf := Sweatfile{Hooks: &Hooks{Create: &cmd}}

	err := sf.RunCreateHook(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(output)
	got := strings.TrimSpace(string(data))
	if got != dir {
		t.Errorf("WORKTREE env: got %q, want %q", got, dir)
	}
}

func TestRunCreateHookFailureReturnsError(t *testing.T) {
	dir := t.TempDir()

	cmd := "exit 1"
	sf := Sweatfile{Hooks: &Hooks{Create: &cmd}}

	err := sf.RunCreateHook(dir)
	if err == nil {
		t.Error("expected error from failing create hook")
	}
}

func TestRunCreateHookNilIsNoop(t *testing.T) {
	dir := t.TempDir()
	sf := Sweatfile{}

	err := sf.RunCreateHook(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunCreateHookEmptyStringIsNoop(t *testing.T) {
	dir := t.TempDir()
	empty := ""
	sf := Sweatfile{Hooks: &Hooks{Create: &empty}}

	err := sf.RunCreateHook(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunCreateHookMultilineWithEmptyLines(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "hook-ran")

	// A multiline create hook with empty lines between commands should
	// execute correctly — empty lines must not be fed to the shell as
	// separate commands or cause parse failures.
	cmd := fmt.Sprintf("echo first\n\ntouch %s\n", marker)
	sf := Sweatfile{Hooks: &Hooks{Create: &cmd}}

	err := sf.RunCreateHook(dir)
	if err != nil {
		t.Fatalf("multiline create hook with empty lines should not error: %v", err)
	}

	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("expected multiline create hook to execute and create marker file")
	}
}

func TestRunPreMergeHookExecutes(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pre-merge-ran")

	cmd := "touch " + marker
	sf := Sweatfile{Hooks: &Hooks{PreMerge: &cmd}}

	err := sf.RunPreMergeHook(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("expected pre-merge hook to run and create marker file")
	}
}

func TestRunPreMergeHookReceivesWorktreeEnv(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "worktree-env")

	cmd := "printenv WORKTREE > " + marker
	sf := Sweatfile{Hooks: &Hooks{PreMerge: &cmd}}

	err := sf.RunPreMergeHook(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("reading marker: %v", err)
	}
	if strings.TrimSpace(string(content)) != dir {
		t.Errorf("expected WORKTREE=%s, got %q", dir, string(content))
	}
}

func TestRunPreMergeHookFailureReturnsError(t *testing.T) {
	dir := t.TempDir()

	cmd := "exit 1"
	sf := Sweatfile{Hooks: &Hooks{PreMerge: &cmd}}

	err := sf.RunPreMergeHook(dir)
	if err == nil {
		t.Error("expected error from failing pre-merge hook")
	}
}

func TestRunPreMergeHookNilIsNoop(t *testing.T) {
	dir := t.TempDir()
	sf := Sweatfile{}

	err := sf.RunPreMergeHook(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunPreMergeHookEmptyStringIsNoop(t *testing.T) {
	dir := t.TempDir()
	empty := ""
	sf := Sweatfile{Hooks: &Hooks{PreMerge: &empty}}

	err := sf.RunPreMergeHook(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
