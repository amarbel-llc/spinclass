package sweatfile

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

type Claude struct {
	SystemPrompt       *string  `toml:"system-prompt"`
	SystemPromptAppend *string  `toml:"system-prompt-append"`
	Allow              []string `toml:"allow"`
}

type Git struct {
	Excludes []string `toml:"excludes"`
}

type Direnv struct {
	Envrc  []string          `toml:"envrc"`
	Dotenv map[string]string `toml:"dotenv"`
}

type SessionEntry struct {
	Start  []string `toml:"start"`
	Resume []string `toml:"resume"`
}

type Hooks struct {
	Create               *string `toml:"create"`
	Stop                 *string `toml:"stop"`
	PreMerge             *string `toml:"pre-merge"`
	DisallowMainWorktree *bool   `toml:"disallow-main-worktree"`
	ToolUseLog           *bool   `toml:"tool-use-log"`
}

//go:generate tommy generate
type Sweatfile struct {
	Claude       *Claude       `toml:"claude"`
	Git          *Git          `toml:"git"`
	Direnv       *Direnv       `toml:"direnv"`
	Hooks        *Hooks        `toml:"hooks"`
	SessionEntry *SessionEntry `toml:"session-entry"`
}

func (sf Sweatfile) StopHookCommand() *string {
	if sf.Hooks == nil {
		return nil
	}
	return sf.Hooks.Stop
}

func (sf Sweatfile) CreateHookCommand() *string {
	if sf.Hooks == nil {
		return nil
	}
	return sf.Hooks.Create
}

func (sf Sweatfile) PreMergeHookCommand() *string {
	if sf.Hooks == nil {
		return nil
	}
	return sf.Hooks.PreMerge
}

func (sf Sweatfile) DisallowMainWorktreeEnabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.DisallowMainWorktree != nil &&
		*sf.Hooks.DisallowMainWorktree
}

// GitExcludes returns the merged git exclude patterns, or nil if none.
func (sf Sweatfile) GitExcludes() []string {
	if sf.Git == nil {
		return nil
	}
	return sf.Git.Excludes
}

func (sf Sweatfile) ToolUseLogEnabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.ToolUseLog != nil &&
		*sf.Hooks.ToolUseLog
}

func (sf Sweatfile) SessionStart() []string {
	if sf.SessionEntry != nil && len(sf.SessionEntry.Start) > 0 {
		return sf.SessionEntry.Start
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return []string{shell}
}

func (sf Sweatfile) SessionResume() []string {
	if sf.SessionEntry != nil && len(sf.SessionEntry.Resume) > 0 {
		return sf.SessionEntry.Resume
	}
	return nil
}

// baseline excludes and allow rules that are always applied regardless of user
// sweatfile config.
func GetDefault() Sweatfile {
	sf := Sweatfile{
		Git: &Git{Excludes: []string{".worktrees/", ".spinclass/", ".mcp.json"}},
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		claudeDir := filepath.Join(home, ".claude")
		sf.Claude = &Claude{Allow: []string{fmt.Sprintf("Read(%s/*)", claudeDir)}}
	}

	return sf
}

func collectSystemPromptAppend(cwd string) (string, error) {
	pattern := filepath.Join(cwd, ".spinclass", "system_prompt_append.d", "*.md")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("globbing system_prompt_append.d: %w", err)
	}

	sort.Strings(matches)

	var parts []string
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", filepath.Base(path), err)
		}
		if content := strings.TrimSpace(string(data)); content != "" {
			parts = append(parts, content)
		}
	}

	return strings.Join(parts, "\n\n"), nil
}

func (sweatfile Sweatfile) ExecClaude(
	args ...string,
) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	scDir := filepath.Join(cwd, ".spinclass")
	if _, err := os.Stat(scDir); os.IsNotExist(err) {
		return fmt.Errorf(".spinclass directory not found in %s; exec-claude requires a spinclass session", cwd)
	}

	// Write user sweatfile system-prompt-append into the .d/ directory
	if sweatfile.Claude != nil && sweatfile.Claude.SystemPromptAppend != nil {
		userContent := resolvePathOrString(*sweatfile.Claude.SystemPromptAppend)
		userPath := filepath.Join(scDir, "system_prompt_append.d", "2-user.md")
		if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
			return fmt.Errorf("creating system_prompt_append.d: %w", err)
		}
		if err := os.WriteFile(userPath, []byte(userContent), 0o644); err != nil {
			return fmt.Errorf("writing user system prompt append: %w", err)
		}
	}

	// Collect all system prompt append fragments
	appendContent, err := collectSystemPromptAppend(cwd)
	if err != nil {
		return err
	}

	if appendContent != "" {
		args = append(
			[]string{"--append-system-prompt", appendContent},
			args...,
		)
	}

	// system-prompt (non-append) still works as before
	if sweatfile.Claude != nil && sweatfile.Claude.SystemPrompt != nil {
		args = append(
			[]string{
				"--system-prompt",
				resolvePathOrString(*sweatfile.Claude.SystemPrompt),
			},
			args...,
		)
	}

	pathGitDirCommon, err := getGitDirCommon()
	if err != nil {
		return err
	}

	pathSweatfileBin := filepath.Join(pathGitDirCommon, "spinclass/bin/")

	envVarPath := filepath.SplitList(os.Getenv("PATH"))
	envVarPath = slices.DeleteFunc(envVarPath, func(value string) bool {
		return filepath.Clean(value) == pathSweatfileBin
	})
	os.Setenv("PATH", strings.Join(envVarPath, string(filepath.ListSeparator)))

	cmdClaude := exec.Command("claude", args...)
	cmdClaude.Stdout = os.Stdout
	cmdClaude.Stderr = os.Stderr
	cmdClaude.Stdin = os.Stdin

	if err := cmdClaude.Run(); err != nil {
		return err
	}

	return nil
}
