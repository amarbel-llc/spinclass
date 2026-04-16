package sweatfile

import (
	"fmt"
	"os"
	"path/filepath"
)

type Claude struct {
	Allow []string `toml:"allow"`
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

// MCPServerDef declares an MCP server to register and auto-approve
// in Claude Code sessions. See CLAUDE.md "MCP Sweatfile Config" design.
type MCPServerDef struct {
	Name      string            `toml:"name"`
	Command   string            `toml:"command"`
	Args      []string          `toml:"args"`
	Env map[string]string `toml:"env"`
}

// StartCommand declares a user-defined `sc start-<name>` subcommand.
// See CLAUDE.md "Custom start commands" for the full schema.
type StartCommand struct {
	Name            string   `toml:"name"`
	Description     string   `toml:"description"`
	ArgName         string   `toml:"arg-name"`
	ArgHelp         string   `toml:"arg-help"`
	ArgRegex        *string  `toml:"arg-regex"`
	ExecCompletions []string `toml:"exec-completions"`
	ExecStart       []string `toml:"exec-start"`
}

//go:generate tommy generate
type Sweatfile struct {
	Claude        *Claude        `toml:"claude"`
	Git           *Git           `toml:"git"`
	Direnv        *Direnv        `toml:"direnv"`
	Hooks         *Hooks         `toml:"hooks"`
	SessionEntry  *SessionEntry  `toml:"session-entry"`
	StartCommands []StartCommand `toml:"start-commands"`
	AllowedMCPs   []string       `toml:"allowed-mcps"`
	MCPs          []MCPServerDef `toml:"mcps"`
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

// EffectiveAllowedMCPs returns the deduplicated list of MCP server names
// that should be auto-approved. Combines explicit allowed-mcps entries
// with implicit names from [[mcps]] entries that have a non-empty command.
func (sf Sweatfile) EffectiveAllowedMCPs() []string {
	seen := make(map[string]bool)
	var result []string

	for _, name := range sf.AllowedMCPs {
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}

	for _, mcp := range sf.MCPs {
		if mcp.Command != "" && !seen[mcp.Name] {
			seen[mcp.Name] = true
			result = append(result, mcp.Name)
		}
	}

	return result
}

// ActiveMCPs returns only [[mcps]] entries with a non-empty command
// (i.e., excluding removal sentinels).
func (sf Sweatfile) ActiveMCPs() []MCPServerDef {
	var active []MCPServerDef
	for _, mcp := range sf.MCPs {
		if mcp.Command != "" {
			active = append(active, mcp)
		}
	}
	return active
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
		Git:           &Git{Excludes: []string{".worktrees/", ".spinclass/", ".mcp.json"}},
		StartCommands: defaultStartCommands(),
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		claudeDir := filepath.Join(home, ".claude")
		sf.Claude = &Claude{Allow: []string{fmt.Sprintf("Read(%s/*)", claudeDir)}}
	}

	return sf
}

// defaultStartCommands returns the baked-in `[[start-commands]]` entries
// that ship with every spinclass install. These exist as tracer bullets:
// commands that used to be hard-coded Go handlers are now declared via the
// same config mechanism users have for custom start-* commands.
func defaultStartCommands() []StartCommand {
	issueRegex := `^[0-9]+$`
	return []StartCommand{
		{
			Name:        "gh_issue",
			Description: "Start a session with a GitHub issue",
			ArgName:     "issue",
			ArgHelp:     "Issue number",
			ArgRegex:    &issueRegex,
			ExecCompletions: []string{
				"sh", "-c",
				`gh issue list --json number,title --limit 20 2>/dev/null | ` +
					`jq '[.[] | {arg: (.number | tostring), description: .title}]' 2>/dev/null`,
			},
			ExecStart: []string{
				"sh", "-c",
				`gh issue view {arg} --json number,title,state,labels,url,body | ` +
					`jq '{context: ("# GitHub Issue Context\n\nThis session is working on the following GitHub issue.\n\n## Issue #" + (.number | tostring) + ": " + .title + "\n- **State:** " + .state + (if (.labels | length) > 0 then "\n- **Labels:** " + ([.labels[].name] | join(", ")) else "" end) + "\n- **URL:** " + .url + "\n\n## Description\n\n" + .body)}'`,
			},
		},
	}
}

