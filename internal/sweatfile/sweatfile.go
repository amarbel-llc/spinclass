package sweatfile

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
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
	Start              []string          `toml:"start"`
	Resume             []string          `toml:"resume"`
	Env                map[string]string `toml:"env"`
	LivenessProbe      []string          `toml:"liveness-probe"`
	TombstoneRetention string            `toml:"tombstone-retention"`
}

type Hooks struct {
	Create                    *string `toml:"create"`
	Stop                      *string `toml:"stop"`
	PreMerge                  *string `toml:"pre-merge"`
	OnAttach                  *string `toml:"on-attach"`
	OnDetach                  *string `toml:"on-detach"`
	DisallowMainWorktree      *bool   `toml:"disallow-main-worktree"`
	ToolUseLog                *bool   `toml:"tool-use-log"`
	DisableMerge              *bool   `toml:"disable-merge"`
	DisableNixGC              *bool   `toml:"disable-nix-gc"`
	DisableImplicitSessions   *bool   `toml:"disable-implicit-sessions"`
	DisableMergeBuildWorktree *bool   `toml:"disable-merge-build-worktree"`
	PreMergeOutputFormat      *string `toml:"pre-merge-output-format"`
	InactivityTimeout         *string `toml:"inactivity-timeout"`
}

// MCPServerDef declares an MCP server to register and auto-approve
// in Claude Code sessions. See CLAUDE.md "MCP Sweatfile Config" design.
type MCPServerDef struct {
	Name    string            `toml:"name"`
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
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

// Remote declares a host whose spinclass sessions appear in sc list and
// completion under a "<name>:" prefix and can be reattached via sc resume.
// See docs/plans/2026-06-06-remote-sessions-design.md.
type Remote struct {
	Name   string   `toml:"name"`
	SSH    string   `toml:"ssh"`    // ssh destination; empty = Name
	Attach []string `toml:"attach"` // argv template; {ssh}/{id} substituted; empty = default
	// Remove marks the entry as an explicit removal sentinel against an
	// inherited remote of the same name. This deliberately diverges from
	// the [[mcps]] name-only-removes precedent: a name-only [[remotes]]
	// entry is the most natural declaration of an all-defaults remote
	// (~/.ssh/config does the work), so removal must be explicit.
	Remove bool `toml:"remove"`
}

// Dest returns the ssh destination for the remote: the explicit `ssh`
// value when set, otherwise the remote's name (~/.ssh/config does the work).
func (r Remote) Dest() string {
	if r.SSH != "" {
		return r.SSH
	}
	return r.Name
}

// PreMergeSkill names a Claude Code skill that an agent must address
// before merge-this-session / check-this-session may run the pre-merge
// hook. See docs/features/0007-pre-merge-skill-attestation.md.
type PreMergeSkill struct {
	Name      string `toml:"name"`
	Rationale string `toml:"rationale"`
}

//go:generate tommy generate
type Sweatfile struct {
	Claude         *Claude         `toml:"claude"`
	Git            *Git            `toml:"git"`
	Direnv         *Direnv         `toml:"direnv"`
	Hooks          *Hooks          `toml:"hooks"`
	SessionEntry   *SessionEntry   `toml:"session-entry"`
	StartCommands  []StartCommand  `toml:"start-commands"`
	AllowedMCPs    []string        `toml:"allowed-mcps"`
	MCPs           []MCPServerDef  `toml:"mcps"`
	PreMergeSkills []PreMergeSkill `toml:"pre-merge-skills"`
	Remotes        []Remote        `toml:"remotes"`
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

// InactivityTimeoutValue returns the parsed [hooks].inactivity-timeout, or 0
// when unset, empty, or unparseable. A non-zero value arms the pre-merge hook
// watchdog in RunPreMergeHookContext: the hook is killed after this span with
// no output (stdout+stderr). Validation (sc validate) rejects an unparseable
// value up front; the runtime parse here degrades to 0 (disabled) so a bad
// value can never silently block a merge.
func (sf Sweatfile) InactivityTimeoutValue() time.Duration {
	if sf.Hooks == nil || sf.Hooks.InactivityTimeout == nil {
		return 0
	}
	d, err := time.ParseDuration(*sf.Hooks.InactivityTimeout)
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// PreMergeOutputFormatValue returns the configured format for the
// pre-merge hook's output, defaulting to "raw" when unset or empty.
// Valid values: "raw", "tap-ndjson", "ndjson-crap". Validation lives in
// internal/validate.
func (sf Sweatfile) PreMergeOutputFormatValue() string {
	if sf.Hooks == nil || sf.Hooks.PreMergeOutputFormat == nil {
		return "raw"
	}
	v := *sf.Hooks.PreMergeOutputFormat
	if v == "" {
		return "raw"
	}
	return v
}

func (sf Sweatfile) OnAttachHookCommand() *string {
	if sf.Hooks == nil {
		return nil
	}
	return sf.Hooks.OnAttach
}

func (sf Sweatfile) OnDetachHookCommand() *string {
	if sf.Hooks == nil {
		return nil
	}
	return sf.Hooks.OnDetach
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

// ActivePreMergeSkills returns only [[pre-merge-skills]] entries with a
// non-empty rationale (i.e., excluding removal sentinels).
func (sf Sweatfile) ActivePreMergeSkills() []PreMergeSkill {
	var active []PreMergeSkill
	for _, s := range sf.PreMergeSkills {
		if s.Rationale != "" {
			active = append(active, s)
		}
	}
	return active
}

// ActiveRemotes returns [[remotes]] entries that are not explicit
// `remove = true` sentinels. A name-only entry is active: it declares an
// all-defaults remote (~/.ssh/config does the work).
func (sf Sweatfile) ActiveRemotes() []Remote {
	var active []Remote
	for _, r := range sf.Remotes {
		if !r.Remove {
			active = append(active, r)
		}
	}
	return active
}

func (sf Sweatfile) ToolUseLogEnabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.ToolUseLog != nil &&
		*sf.Hooks.ToolUseLog
}

func (sf Sweatfile) DisableMergeEnabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.DisableMerge != nil &&
		*sf.Hooks.DisableMerge
}

func (sf Sweatfile) DisableNixGCEnabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.DisableNixGC != nil &&
		*sf.Hooks.DisableNixGC
}

func (sf Sweatfile) DisableImplicitSessionsEnabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.DisableImplicitSessions != nil &&
		*sf.Hooks.DisableImplicitSessions
}

// MergeBuildWorktreeDisabled reports whether [hooks].disable-merge-build-worktree
// is true. When false (the default), the pre-merge hook runs in a transient
// detached worktree pinned to the merged sha; when true it runs in place in the
// session worktree (legacy behavior). See spinclass-sweatfile(5).
func (sf Sweatfile) MergeBuildWorktreeDisabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.DisableMergeBuildWorktree != nil &&
		*sf.Hooks.DisableMergeBuildWorktree
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

// SessionEnv returns the user-configured environment variables to inject
// into the session's process environment. These are exposed to
// `[session-entry].start`/`resume` argv expansion, to lifecycle hooks,
// and to the liveness probe. spinclass-owned vars (SPINCLASS_SESSION_ID,
// SPINCLASS_REPO, SPINCLASS_BRANCH, SPINCLASS_WORKTREE,
// SPINCLASS_DESCRIPTION, TMPDIR, CLAUDE_CODE_TMPDIR) are applied AFTER
// this map and so cannot be clobbered by user config.
//
// Typical use: set $SPINCLASS_GROUP for a zmx-style multiplexer so the
// probe and entrypoint argv can reference it symbolically. Returns nil
// when no [session-entry].env is configured.
func (sf Sweatfile) SessionEnv() map[string]string {
	if sf.SessionEntry == nil {
		return nil
	}
	return sf.SessionEntry.Env
}

// SessionLivenessProbe returns the argv list used to determine whether
// a multiplexer-managed session is still attachable after the entrypoint
// returns. Empty argv means no probe is configured — callers should treat
// the absence of a probe as "session is dead" (the conservative answer).
func (sf Sweatfile) SessionLivenessProbe() []string {
	if sf.SessionEntry == nil {
		return nil
	}
	return sf.SessionEntry.LivenessProbe
}

// TombstoneRetention parses `[session-entry].tombstone-retention` into a
// duration. Returns (d, true) when the value is set and parses cleanly,
// (0, false) when unset, and (0, true) when set to "0" (explicit
// "tombstones never expire"). Parse errors return (0, false) — callers
// should fall back to session.DefaultTombstoneRetention().
func (sf Sweatfile) TombstoneRetention() (time.Duration, bool) {
	if sf.SessionEntry == nil || sf.SessionEntry.TombstoneRetention == "" {
		return 0, false
	}
	d, err := time.ParseDuration(sf.SessionEntry.TombstoneRetention)
	if err != nil {
		return 0, false
	}
	return d, true
}

// baseline excludes and allow rules that are always applied regardless of user
// sweatfile config.
func GetDefault() Sweatfile {
	sf := Sweatfile{
		// Every path spinclass (or a tool it invokes) writes into a
		// worktree root is excluded so it never shows as untracked or
		// gets accidentally staged (#116, #119): .spinclass/ holds all
		// spinclass-owned data including the [session-entry].env dotenv
		// file (#121), .envrc is truncate-rewritten by writeEnvrc
		// whenever direnv resolves (.direnv/ is direnv's own cache it
		// then creates), .tmp/ is the session scratch dir, and
		// .claude/settings.local.json carries the claude-allow rules.
		// Without these the worktree only looks clean on machines whose
		// personal global gitignore happens to cover them.
		Git: &Git{Excludes: []string{
			".worktrees/", ".spinclass/", ".mcp.json",
			".envrc", ".direnv/", ".tmp/", ".claude/settings.local.json",
		}},
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
		{
			Name:        "gh_pr",
			Description: "Start a session from a GitHub pull request",
			ArgName:     "pr",
			ArgHelp:     "PR number or GitHub URL",
			ExecCompletions: []string{
				"sh", "-c",
				`gh pr list --json number,title --limit 20 2>/dev/null | ` +
					`jq '[.[] | {arg: (.number | tostring), description: .title}]' 2>/dev/null`,
			},
			ExecStart: []string{
				"sh", "-c",
				`gh pr view {arg} --json headRefName,isCrossRepository,title,number,url,body,state,labels | ` +
					`jq 'if .isCrossRepository then error("fork PRs are not supported (PR #\(.number) is from a fork)") else ` +
					`{branch: .headRefName, description: ("\(.title) (#\(.number))"), context: ` +
					`("# Pull Request Context\n\nThis session is working on the following pull request.\n\n## PR #" + (.number | tostring) + ": " + .title + ` +
					`"\n- **State:** " + .state + ` +
					`(if (.labels | length) > 0 then "\n- **Labels:** " + ([.labels[].name] | join(", ")) else "" end) + ` +
					`"\n- **URL:** " + .url + "\n\n## Description\n\n" + .body)} end'`,
			},
		},
	}
}
