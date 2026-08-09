package sweatfile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/embeds"
	"code.linenisgreat.com/spinclass/internal/testfs"
	"code.linenisgreat.com/spinclass/internal/testgit"
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

func TestHardcodedDefaultsGitExcludes(t *testing.T) {
	defaults := GetDefault()

	if defaults.Git == nil {
		t.Fatal("expected non-nil Git struct")
	}

	if defaults.Git.Excludes == nil {
		t.Fatal("expected non-nil git excludes slice")
	}

	// Every path spinclass (or a tool it invokes) writes into a worktree
	// must be excluded so it never shows as untracked / gets accidentally
	// staged (#116, #119): .envrc and .direnv/ come from the direnv
	// integration, .tmp/ is the session scratch dir, and
	// .claude/settings.local.json carries the claude-allow rules. The
	// [session-entry].env dotenv file lives inside .spinclass/ (#121).
	want := []string{
		".worktrees/", ".spinclass/", ".mcp.json",
		".envrc", ".direnv/", ".tmp/", ".claude/settings.local.json",
	}
	if len(defaults.Git.Excludes) != len(want) {
		t.Fatalf(
			"expected %d git excludes, got %d: %v",
			len(want),
			len(defaults.Git.Excludes),
			defaults.Git.Excludes,
		)
	}
	for i, w := range want {
		if defaults.Git.Excludes[i] != w {
			t.Errorf("excludes[%d]: expected %q, got %q", i, w, defaults.Git.Excludes[i])
		}
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
	if len(allowRaw) != 5 {
		t.Fatalf(
			"expected 5 rules (3 passed + 2 scoped), got %d: %v",
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

	// Last 2 are auto-injected scoped rules. There is deliberately no
	// Write(...) rule: Edit(path) already covers every file-editing tool,
	// and newer Claude Code rejects path-scoped Write rules at startup.
	readRule, _ := allowRaw[3].(string)
	editRule, _ := allowRaw[4].(string)

	wantRead := "Read(" + dir + "/*)"
	wantEdit := "Edit(" + dir + "/*)"
	if readRule != wantRead {
		t.Errorf("read rule: got %q, want %q", readRule, wantRead)
	}
	if editRule != wantEdit {
		t.Errorf("edit rule: got %q, want %q", editRule, wantEdit)
	}
	for _, rule := range allowRaw {
		if s, _ := rule.(string); strings.HasPrefix(s, "Write(") {
			t.Errorf("unexpected path-scoped Write rule: %q", s)
		}
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
	testfs.MustUnmarshal(t, data, &doc)
	permsMap, _ := doc["permissions"].(map[string]any)
	allowRaw, _ := permsMap["allow"].([]any)

	// Even with no passed rules, the 2 scoped rules are injected
	if len(allowRaw) != 2 {
		t.Fatalf("expected 2 scoped rules, got %d: %v", len(allowRaw), allowRaw)
	}
}

func TestApplyClaudeSettingsOverwritesExistingKeys(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	testfs.MustMkdirAll(t, claudeDir, 0o755)

	existing := map[string]any{
		"mcpServers": map[string]any{"test": true},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	testfs.MustWriteFile(t, filepath.Join(claudeDir, "settings.local.json"), data, 0o644)

	err := ApplyClaudeSettings(dir, Sweatfile{Claude: &Claude{Allow: []string{"Read"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, _ := os.ReadFile(filepath.Join(claudeDir, "settings.local.json"))
	var doc map[string]any
	testfs.MustUnmarshal(t, result, &doc)

	if _, ok := doc["mcpServers"]; ok {
		t.Error("expected mcpServers key to be overwritten")
	}
}

// Plugin-level hook registration (hooks/hooks.json shipped via flake.nix
// postInstall) replaces the per-worktree hook block that ApplyClaudeSettings
// used to write into settings.local.json. Confirm that the session-local
// settings file no longer contains a "hooks" key in either a worktree or a
// main repo, regardless of sweatfile [hooks] config.
func TestApplyClaudeSettingsNeverWritesHooksKey(t *testing.T) {
	cases := []struct {
		name   string
		gitFn  func(dir string)
		stop   string
		toolUL bool
	}{
		{
			name: "worktree empty sweatfile",
			gitFn: func(dir string) {
				_ = os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /tmp/fake"), 0o644)
			},
		},
		{
			name: "worktree with stop hook configured",
			gitFn: func(dir string) {
				_ = os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /tmp/fake"), 0o644)
			},
			stop: "just test",
		},
		{
			name: "worktree with tool-use-log enabled",
			gitFn: func(dir string) {
				_ = os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /tmp/fake"), 0o644)
			},
			toolUL: true,
		},
		{
			name: "main repo",
			gitFn: func(dir string) {
				_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.gitFn(dir)

			sf := Sweatfile{}
			if tc.stop != "" || tc.toolUL {
				sf.Hooks = &Hooks{}
				if tc.stop != "" {
					sf.Hooks.Stop = &tc.stop
				}
				if tc.toolUL {
					sf.Hooks.ToolUseLog = &tc.toolUL
				}
			}

			if err := ApplyClaudeSettings(dir, sf); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			data, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
			var doc map[string]any
			testfs.MustUnmarshal(t, data, &doc)

			if _, ok := doc["hooks"]; ok {
				t.Errorf("expected no hooks key in settings.local.json (plugin-level registration replaces it); got %v", doc["hooks"])
			}
		})
	}
}

func TestPrepareDirenvWritesEnvrcWithoutUseFlakeWhenNoFlakeNix(t *testing.T) {
	dir := t.TempDir()
	testgit.MustInit(t, dir)

	// Create a fake direnv that just exits 0
	fakeBin := t.TempDir()
	fakeDirenv := filepath.Join(fakeBin, "direnv")
	testfs.MustWriteFile(t, fakeDirenv, []byte("#!/bin/sh\nexit 0\n"), 0o755)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+":"+gitDir(t))
	defer func() { _ = os.Setenv("PATH", origPath) }()

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

func TestPrepareDirenvPrefersEmbeddedOverPath(t *testing.T) {
	dir := t.TempDir()
	testgit.MustInit(t, dir)

	// Embedded fake direnv: records its invocation by writing a marker
	// file. PATH-only fake direnv: would write a *different* marker.
	// We assert the embedded one ran and the PATH one did not.
	// Shell-only marker creation (`: > "$path"`) so the script
	// doesn't depend on `touch` or other coreutils being on the
	// process's PATH — important for the nix sandbox where PATH
	// is whatever t.Setenv last wrote.
	embeddedBin := t.TempDir()
	embeddedDirenv := filepath.Join(embeddedBin, "direnv")
	embeddedMarker := filepath.Join(embeddedBin, "ran")
	embeddedScript := "#!/bin/sh\n: > \"" + embeddedMarker + "\"\nexit 0\n"
	if err := os.WriteFile(embeddedDirenv, []byte(embeddedScript), 0o755); err != nil {
		t.Fatal(err)
	}

	pathBin := t.TempDir()
	pathDirenv := filepath.Join(pathBin, "direnv")
	pathMarker := filepath.Join(pathBin, "ran")
	pathScript := "#!/bin/sh\n: > \"" + pathMarker + "\"\nexit 0\n"
	if err := os.WriteFile(pathDirenv, []byte(pathScript), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", pathBin+":"+gitDir(t))

	prevMadder, prevDirenv, prevDodder := embeds.MadderBin(), embeds.DirenvBin(), embeds.DodderBin()
	embeds.Set(prevMadder, embeddedDirenv, prevDodder)
	t.Cleanup(func() { embeds.Set(prevMadder, prevDirenv, prevDodder) })

	if err := (Sweatfile{}).prepareDirenv(dir); err != nil {
		t.Fatalf("prepareDirenv: %v", err)
	}

	if _, err := os.Stat(embeddedMarker); err != nil {
		t.Errorf("expected embedded direnv to be invoked, marker stat err=%v", err)
	}
	if _, err := os.Stat(pathMarker); !os.IsNotExist(err) {
		t.Errorf("expected PATH direnv NOT to be invoked, marker stat err=%v", err)
	}
}

func TestPrepareDirenvSkipsWhenDirenvNotInPath(t *testing.T) {
	dir := t.TempDir()
	testfs.MustWriteFile(t, filepath.Join(dir, "flake.nix"), []byte("{}"), 0o644)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	defer func() { _ = os.Setenv("PATH", origPath) }()

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
	testgit.MustInit(t, dir)
	testfs.MustWriteFile(t, filepath.Join(dir, "flake.nix"), []byte("{}"), 0o644)

	// Create a fake direnv that just exits 0
	fakeBin := t.TempDir()
	fakeDirenv := filepath.Join(fakeBin, "direnv")
	testfs.MustWriteFile(t, fakeDirenv, []byte("#!/bin/sh\nexit 0\n"), 0o755)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+":"+gitDir(t))
	defer func() { _ = os.Setenv("PATH", origPath) }()

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
	testgit.MustInit(t, dir)
	testfs.MustWriteFile(t, filepath.Join(dir, "flake.nix"), []byte("{}"), 0o644)
	testfs.MustWriteFile(t, filepath.Join(dir, ".envrc"), []byte("old content\n"), 0o644)

	fakeBin := t.TempDir()
	fakeDirenv := filepath.Join(fakeBin, "direnv")
	testfs.MustWriteFile(t, fakeDirenv, []byte("#!/bin/sh\nexit 0\n"), 0o755)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+":"+gitDir(t))
	defer func() { _ = os.Setenv("PATH", origPath) }()

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
	testgit.MustInit(t, dir)

	fakeBin := t.TempDir()
	testfs.MustWriteFile(
		t,
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
	testgit.MustInit(t, dir)
	testfs.MustWriteFile(t, filepath.Join(dir, "flake.nix"), []byte("{}"), 0o644)

	fakeBin := t.TempDir()
	testfs.MustWriteFile(
		t,
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
	testgit.MustInit(t, dir)

	fakeBin := t.TempDir()
	testfs.MustWriteFile(
		t,
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
	testgit.MustInit(t, dir)

	fakeBin := t.TempDir()
	testfs.MustWriteFile(
		t,
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

	data, err := os.ReadFile(filepath.Join(dir, ".spinclass", "env"))
	if err != nil {
		t.Fatalf("reading .spinclass/env: %v", err)
	}

	content := string(data)
	if content != "BAZ=qux\nFOO=bar\n" {
		t.Errorf(".spinclass/env content: got %q", content)
	}
}

func TestWriteSpinclassEnvInterpolatesWorktree(t *testing.T) {
	dir := t.TempDir()
	testgit.MustInit(t, dir)

	fakeBin := t.TempDir()
	testfs.MustWriteFile(
		t,
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

	data, _ := os.ReadFile(filepath.Join(dir, ".spinclass", "env"))
	want := fmt.Sprintf("INCLUDE_PATH=%s/lib:.\n", dir)
	if string(data) != want {
		t.Errorf(
			".spinclass/env content:\ngot  %q\nwant %q",
			string(data),
			want,
		)
	}
}

func TestEnvAutoDotenvDirective(t *testing.T) {
	dir := t.TempDir()
	testgit.MustInit(t, dir)

	fakeBin := t.TempDir()
	testfs.MustWriteFile(
		t,
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
	if !strings.Contains(content, "dotenv .spinclass/env") {
		t.Errorf("expected dotenv .spinclass/env in .envrc, got %q", content)
	}
}

// A worktree created by a pre-#121 spinclass has the dotenv file at the
// old top-level path; Apply removes it even when no dotenv is configured
// so the stale file can't linger after the location moved.
func TestApplyRemovesStaleSpinclassEnv(t *testing.T) {
	dir := t.TempDir()
	testgit.MustInit(t, dir)

	fakeBin := t.TempDir()
	testfs.MustWriteFile(
		t,
		filepath.Join(fakeBin, "direnv"),
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	)
	t.Setenv("PATH", fakeBin+":"+gitDir(t))

	stale := filepath.Join(dir, ".spinclass.env")
	testfs.MustWriteFile(t, stale, []byte("FOO=bar\n"), 0o644)

	if err := (Sweatfile{}).Apply(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("expected stale .spinclass.env to be removed, stat err: %v", err)
	}
}

func TestNoEnvNoDotenvDirective(t *testing.T) {
	dir := t.TempDir()
	testgit.MustInit(t, dir)

	fakeBin := t.TempDir()
	testfs.MustWriteFile(
		t,
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

	err := sf.RunCreateHook(dir, io.Discard)
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

	err := sf.RunCreateHook(dir, io.Discard)
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

	err := sf.RunCreateHook(dir, io.Discard)
	if err == nil {
		t.Error("expected error from failing create hook")
	}
}

func TestRunCreateHookNilIsNoop(t *testing.T) {
	dir := t.TempDir()
	sf := Sweatfile{}

	err := sf.RunCreateHook(dir, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunCreateHookEmptyStringIsNoop(t *testing.T) {
	dir := t.TempDir()
	empty := ""
	sf := Sweatfile{Hooks: &Hooks{Create: &empty}}

	err := sf.RunCreateHook(dir, io.Discard)
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

	err := sf.RunCreateHook(dir, io.Discard)
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

	err := sf.RunPreMergeHook(dir, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("expected pre-merge hook to run and create marker file")
	}
}

// With a job id in ctx (the pre-merge hook's #25 scope signal) but the scope
// tier disabled, the hook must run BARE — the systemd-run wrap is a no-op when
// ScopeArgv reports unavailable, so a host without a systemd user bus (or with
// RINGMASTER_DISABLE_SCOPE) still runs the hook normally. Guards against a
// scopeJobID-set path accidentally prepending a prefix that isn't runnable. The
// wrap-active path needs a live user bus and is dogfooded, not covered here.
func TestRunPreMergeHookScopeDisabledRunsBare(t *testing.T) {
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	dir := t.TempDir()
	marker := filepath.Join(dir, "pre-merge-ran")

	cmd := "touch " + marker
	sf := Sweatfile{Hooks: &Hooks{PreMerge: &cmd}}

	ctx := clown.WithJobID(context.Background(), "merge-9f3c1a2b")
	if err := sf.RunPreMergeHookContext(ctx, dir, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("expected pre-merge hook to run bare (scope disabled) and create marker")
	}
}

// TestRunPreMergeHookScopeActiveWrapsInCgroup validates the wrap-ACTIVE path on
// a host with a systemd user bus: the pre-merge hook runs inside its
// ringmaster-<id>.scope, so the hook process's own cgroup carries that unit
// name. Skips when the scope tier is unavailable (the checkPhase sandbox and
// macOS have no user bus), so it exercises the real path on a Linux dev host and
// is a clean no-op in CI. This is the only automated coverage of the wrap
// actually taking effect.
func TestRunPreMergeHookScopeActiveWrapsInCgroup(t *testing.T) {
	jobID := "merge-scopetest1"
	if _, ok := clown.ScopeArgv(jobID); !ok {
		t.Skip("scope tier unavailable (no systemd user bus); active-path test skipped")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "cgroup")

	cmd := "cat /proc/self/cgroup > " + marker
	sf := Sweatfile{Hooks: &Hooks{PreMerge: &cmd}}

	ctx := clown.WithJobID(context.Background(), jobID)
	if err := sf.RunPreMergeHookContext(ctx, dir, io.Discard); err != nil {
		t.Fatalf("scoped pre-merge hook: %v", err)
	}
	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("reading cgroup marker: %v", err)
	}
	want := clown.ScopeUnitName(jobID)
	if !strings.Contains(string(content), want) {
		t.Errorf("hook cgroup %q does not contain the scope unit %q",
			strings.TrimSpace(string(content)), want)
	}
}

// TestRunPreMergeHookScopeReapsSubtreeOnCancel is the decisive #25 test: it
// proves the scope's control-group kill reaps a hook subtree that IGNORES
// SIGTERM — the exact residual #188's SIGTERM + WaitDelay-SIGKILL + no-Setpgid
// teardown leaves behind. The hook traps SIGTERM and backgrounds a `sleep` that
// holds the inherited pipe; under #188 alone that child is reparented and
// survives the top's SIGKILL, but #25's ScopeStop (`systemctl --user stop
// ringmaster-<id>.scope`) reaps the whole cgroup. Self-skips without a systemd
// user bus (checkPhase sandbox, macOS); runs for real on a Linux dev host.
func TestRunPreMergeHookScopeReapsSubtreeOnCancel(t *testing.T) {
	jobID := fmt.Sprintf("merge-scopekill-%d", time.Now().UnixNano())
	if _, ok := clown.ScopeArgv(jobID); !ok {
		t.Skip("scope tier unavailable (no systemd user bus)")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	// Top ignores SIGTERM; the backgrounded sleep is the stubborn subtree that
	// survives #188's teardown but must not survive #25's scope reap.
	cmd := "trap '' TERM; sleep 300 & echo $! > " + pidFile + "; wait"
	sf := Sweatfile{Hooks: &Hooks{PreMerge: &cmd}}

	ctx, cancel := context.WithCancel(clown.WithJobID(context.Background(), jobID))
	var hookOut bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- sf.RunPreMergeHookContext(ctx, dir, &hookOut) }()

	pid := waitForChildPID(t, pidFile)
	// Belt-and-suspenders: never leak the stubborn child (or its scope) past the
	// test, whatever the outcome.
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_ = clown.ScopeStop(context.Background(), jobID)
	})
	if !processAlive(pid) {
		t.Fatalf("child %d not alive after the hook started", pid)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunPreMergeHookContext did not return within 30s of cancel")
	}
	t.Logf("scope unit: %s\nhook output after cancel:\n%s", clown.ScopeUnitName(jobID), hookOut.String())

	// The hook has returned, so ScopeStop has run; the subtree must be gone.
	deadline := time.Now().Add(15 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d survived cancel — the scope did not reap the subtree (#25 regression)", pid)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// processAlive reports whether pid is a live process (a signal-0 probe).
func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// waitForChildPID polls pidFile until it holds a parseable, positive pid.
func waitForChildPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("child pid file never appeared")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestRunPreMergeHookReceivesWorktreeEnv(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "worktree-env")

	cmd := "printenv WORKTREE > " + marker
	sf := Sweatfile{Hooks: &Hooks{PreMerge: &cmd}}

	err := sf.RunPreMergeHook(dir, io.Discard)
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

	err := sf.RunPreMergeHook(dir, io.Discard)
	if err == nil {
		t.Error("expected error from failing pre-merge hook")
	}
}

func TestRunPreMergeHookNilIsNoop(t *testing.T) {
	dir := t.TempDir()
	sf := Sweatfile{}

	err := sf.RunPreMergeHook(dir, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunPreMergeHookEmptyStringIsNoop(t *testing.T) {
	dir := t.TempDir()
	empty := ""
	sf := Sweatfile{Hooks: &Hooks{PreMerge: &empty}}

	err := sf.RunPreMergeHook(dir, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Regression test for spinclass#27: the hook MUST NOT write to os.Stdout.
// In `spinclass serve` mode, os.Stdout is the JSON-RPC transport; any byte
// the hook emits there corrupts the protocol and the MCP client closes the
// connection. The hook must write to the caller-provided writer instead.
func TestRunHookWritesToWriterNotStdout(t *testing.T) {
	dir := t.TempDir()

	// Swap os.Stdout for a pipe so we can observe whether anything is
	// written to it during the hook.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	var hookOut bytes.Buffer
	cmd := "echo STDOUT_LINE; echo STDERR_LINE 1>&2"
	sf := Sweatfile{Hooks: &Hooks{PreMerge: &cmd}}

	if err := sf.RunPreMergeHook(dir, &hookOut); err != nil {
		t.Fatalf("hook: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	leaked, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if len(leaked) != 0 {
		t.Errorf("hook leaked %d bytes to os.Stdout: %q", len(leaked), string(leaked))
	}

	got := hookOut.String()
	if !strings.Contains(got, "STDOUT_LINE") {
		t.Errorf("writer missing STDOUT_LINE; got %q", got)
	}
	if !strings.Contains(got, "STDERR_LINE") {
		t.Errorf("writer missing STDERR_LINE; got %q", got)
	}
}

func TestApplyClaudeSettingsEnabledMCPs(t *testing.T) {
	dir := t.TempDir()

	sf := Sweatfile{
		AllowedMCPs: []string{"external-server"},
		MCPs: []MCPServerDef{
			{Name: "my-linter", Command: "lint"},
		},
	}

	err := ApplyClaudeSettings(dir, sf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	var doc map[string]any
	testfs.MustUnmarshal(t, data, &doc)

	enabledRaw, _ := doc["enabledMcpjsonServers"].([]any)
	enabled := make(map[string]bool)
	for _, v := range enabledRaw {
		enabled[v.(string)] = true
	}

	// spinclass is loaded via the clown plugin and must not appear
	// in the session-local enabled list.
	if enabled["spinclass"] {
		t.Error("did not expect spinclass in enabledMcpjsonServers (loaded via clown plugin)")
	}
	if !enabled["external-server"] {
		t.Error("expected external-server in enabledMcpjsonServers")
	}
	if !enabled["my-linter"] {
		t.Error("expected my-linter in enabledMcpjsonServers")
	}
}

func TestApplyClaudeSettingsEnabledMCPsDedup(t *testing.T) {
	dir := t.TempDir()

	sf := Sweatfile{
		AllowedMCPs: []string{"foo", "my-linter"},
		MCPs: []MCPServerDef{
			{Name: "my-linter", Command: "lint"},
		},
	}

	err := ApplyClaudeSettings(dir, sf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	var doc map[string]any
	testfs.MustUnmarshal(t, data, &doc)

	enabledRaw, _ := doc["enabledMcpjsonServers"].([]any)
	names := make(map[string]int)
	for _, v := range enabledRaw {
		names[v.(string)]++
	}
	if names["foo"] != 1 {
		t.Errorf("expected foo once, got %d", names["foo"])
	}
	if names["my-linter"] != 1 {
		t.Errorf("expected my-linter once, got %d", names["my-linter"])
	}
}

func TestApplyClaudeSettingsEmptyOmitsEnabledMCPs(t *testing.T) {
	dir := t.TempDir()

	if err := ApplyClaudeSettings(dir, Sweatfile{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	var doc map[string]any
	testfs.MustUnmarshal(t, data, &doc)

	if _, ok := doc["enabledMcpjsonServers"]; ok {
		t.Errorf("expected no enabledMcpjsonServers key when no user MCPs are declared; got %v", doc["enabledMcpjsonServers"])
	}
}

// pinLoggingDirenv installs a fake direnv on PATH that appends its full argv
// (space-joined, one line per invocation) to a log file, and returns the log
// path. Used to assert both WHAT subcommand AllowDirenv invokes (must be a bare
// `allow`, never `exec`) and HOW MANY times it fires.
func pinLoggingDirenv(t *testing.T) (logPath string) {
	t.Helper()
	fakeBin := t.TempDir()
	logPath = filepath.Join(fakeBin, "direnv.log")
	// `printf '%s\n' "$*"` records the whole argv on one line. exit 0 so allow
	// always "succeeds".
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + logPath + "\"\nexit 0\n"
	testfs.MustWriteFile(t, filepath.Join(fakeBin, "direnv"), []byte(script), 0o755)
	t.Setenv("PATH", fakeBin+":"+gitDir(t))
	return logPath
}

func direnvInvocations(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading direnv log: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// TestAllowDirenvBareAndIdempotent is the unit guard for the #213 fix. It
// asserts AllowDirenv (a) invokes a bare `allow` subcommand — NOT wrapped in
// `direnv exec` — and (b) can be called repeatedly (the post-create-hook
// re-allow is a second call over a possibly-mutated .envrc).
func TestAllowDirenvBareAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	testgit.MustInit(t, dir)
	logPath := pinLoggingDirenv(t)

	sf := Sweatfile{}
	// Write the .envrc first (prepareDirenv does one allow), then call
	// AllowDirenv again to mimic the post-create-hook re-allow.
	if err := sf.prepareDirenv(dir); err != nil {
		t.Fatalf("prepareDirenv: %v", err)
	}
	if err := sf.AllowDirenv(dir); err != nil {
		t.Fatalf("AllowDirenv: %v", err)
	}

	invocations := direnvInvocations(t, logPath)
	if len(invocations) != 2 {
		t.Fatalf("expected 2 direnv invocations (initial + re-allow), got %d: %q", len(invocations), invocations)
	}
	for i, argv := range invocations {
		if argv != "allow" {
			t.Errorf("invocation %d: expected bare %q (not wrapped in `direnv exec`), got %q", i, "allow", argv)
		}
	}
}

// TestAllowDirenvNoEnvrcIsNoop asserts AllowDirenv does not invoke direnv when
// the worktree has no .envrc (a non-direnv repo), so the guard keeps it safe to
// call unconditionally from worktree.Create.
func TestAllowDirenvNoEnvrcIsNoop(t *testing.T) {
	dir := t.TempDir()
	testgit.MustInit(t, dir)
	logPath := pinLoggingDirenv(t)

	if err := (Sweatfile{}).AllowDirenv(dir); err != nil {
		t.Fatalf("AllowDirenv: %v", err)
	}

	if invocations := direnvInvocations(t, logPath); len(invocations) != 0 {
		t.Errorf("expected no direnv invocation without a .envrc, got %q", invocations)
	}
}
