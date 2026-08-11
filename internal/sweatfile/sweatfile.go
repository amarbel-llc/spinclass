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
	// SpawnEntry is the detached-harness argv `sc spawn` execs DIRECTLY
	// (FDR-0017 Piece 1: spinclass no longer wraps in a
	// multiplexer — the harness self-detaches and returns promptly, e.g.
	// clown's --clown-attach=spawn). {prompt} = the driver's brief, {dir} =
	// the worker worktree. Defaults to the clown spawn form (SessionSpawnEntry).
	SpawnEntry []string `toml:"spawn-entry"`
	// SpawnWindow is an argv template exec'd fire-and-forget once the worker's
	// hello arrives: it opens a terminal window onto the spawned worker (#149).
	// {id} = the worker's session key, {dir} = the worker worktree, {attach-id}
	// = the worker's posh session id from the hello (for a
	// `posh attach {attach-id}` reattach window, direction B); {entry}/{prompt}
	// are rejected by validate. Unset = no window.
	SpawnWindow []string `toml:"spawn-window"`
	// ModelFlags maps a clown provider name (as selected by spawn-entry's
	// --provider/--provider=) to the CLI flag that provider's binary uses to
	// select a model, e.g. {"claude": "--model"}. Consulted by
	// spawn.SpliceModelFlag when the spawn-session `model` param
	// is set. Per-key merge like Env. Defaults to {"claude": "--model"} — the
	// only mapping verified against an actual provider CLI (forwarded through
	// clown's `--` provider-args boundary). See
	// docs/plans/2026-07-11-spawn-model-selection-design.md.
	ModelFlags map[string]string `toml:"model-flags"`
}

type Hooks struct {
	Create                     *string `toml:"create"`
	Stop                       *string `toml:"stop"`
	PreMerge                   *string `toml:"pre-merge"`
	PostMerge                  *string `toml:"post-merge"`
	Repair                     *string `toml:"repair"`
	PreCommit                  *string `toml:"pre-commit"`
	OnAttach                   *string `toml:"on-attach"`
	OnDetach                   *string `toml:"on-detach"`
	DisallowMainWorktree       *bool   `toml:"disallow-main-worktree"`
	ToolUseLog                 *bool   `toml:"tool-use-log"`
	DisableMerge               *bool   `toml:"disable-merge"`
	DisableMergeQueue          *bool   `toml:"disable-merge-queue"`
	DisableMergeStacking       *bool   `toml:"disable-merge-stacking"`
	DisableRepair              *bool   `toml:"disable-repair"`
	DisablePostMerge           *bool   `toml:"disable-post-merge"`
	DisablePreCommit           *bool   `toml:"disable-pre-commit"`
	DisableNixGC               *bool   `toml:"disable-nix-gc"`
	DisableImplicitSessions    *bool   `toml:"disable-implicit-sessions"`
	DisableMergeBuildWorktree  *bool   `toml:"disable-merge-build-worktree"`
	DisableWorktreePathRewrite *bool   `toml:"disable-worktree-path-rewrite"`
	PreMergeOutputFormat       *string `toml:"pre-merge-output-format"`
	InactivityTimeout          *string `toml:"inactivity-timeout"`
	PostMergeTimeout           *string `toml:"post-merge-timeout"`
	AutoRebuildOnResume        *bool   `toml:"auto-rebuild-on-resume"`
	AllowStaleBase             *bool   `toml:"allow-stale-base"`
}

// Sysprompt configures the dynamic system-prompt fragment spinclass
// contributes to a clown-launched session (internal/sysprompt, FDR 0021).
type Sysprompt struct {
	// DocIndexDirs overrides the dirs the design-record index scans (relative
	// to the worktree/checkout root). Tri-state, but with OVERRIDE semantics
	// rather than the append default of other string arrays — these are scan
	// roots: nil inherits (and, unset at the leaf, the sysprompt package's
	// built-in default dirs apply); a non-empty value replaces the inherited
	// list; an explicit empty list disables the index (the off switch).
	DocIndexDirs []string `toml:"doc-index-dirs"`
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
	Sysprompt      *Sysprompt      `toml:"sysprompt"`
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

// PostMergeHookCommand returns the [hooks].post-merge command, or nil when
// unset. When set (and not disabled via PostMergeDisabled) the merge runs it
// AFTER the merge has landed and been pushed, as the merge's last stage — on
// the queued path still UNDER the per-repo merge lock, so a merge stays
// exclusive end to end and no sibling session can land or deploy mid-hook.
// See FDR 0023.
func (sf Sweatfile) PostMergeHookCommand() *string {
	if sf.Hooks == nil {
		return nil
	}
	return sf.Hooks.PostMerge
}

// DefaultPostMergeTimeout caps how long a post-merge hook may run when
// [hooks].post-merge-timeout is unset. Unlike the pre-merge gate — whose
// watchdog is opt-in and off by default — post-merge runs UNDER the per-repo
// landing lock, so a wedged hook holds the whole repo's merge queue, not just
// its own session. That blast radius is why this one ships capped by default
// (spinclass#246).
//
// 10m is chosen to sit well clear of a real deploy (minutes, longer on a cold
// nix cache) while still bounding a genuine wedge.
const DefaultPostMergeTimeout = 10 * time.Minute

// PostMergeTimeoutValue returns the wall-clock cap for the post-merge hook:
// the parsed [hooks].post-merge-timeout, DefaultPostMergeTimeout when unset or
// empty, and 0 (meaning NO timeout) when explicitly set to a zero duration.
//
// A wall-clock cap rather than an inactivity one: a deploy can legitimately
// produce no output for minutes, so silence is not evidence of a wedge (this
// is the deliberate difference from [hooks].inactivity-timeout).
//
// An unparseable or negative value degrades to the DEFAULT, not to 0. This
// also differs from InactivityTimeoutValue, and for a reason: that knob
// defaults to off, so degrading a bad value to 0 changes nothing, whereas here
// it would silently strip a protection that is on by default. `sc validate`
// rejects a bad value up front; the runtime fallback just refuses to make a
// typo the thing that wedges a repo's merge queue.
func (sf Sweatfile) PostMergeTimeoutValue() time.Duration {
	if sf.Hooks == nil || sf.Hooks.PostMergeTimeout == nil {
		return DefaultPostMergeTimeout
	}
	v := *sf.Hooks.PostMergeTimeout
	if v == "" {
		return DefaultPostMergeTimeout
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return DefaultPostMergeTimeout
	}
	return d
}

// PostMergeDisabled reports whether [hooks].disable-post-merge is true. It
// suppresses an inherited [hooks].post-merge command without having to clear
// the string, mirroring the disable-repair / disable-merge opt-out shape.
func (sf Sweatfile) PostMergeDisabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.DisablePostMerge != nil &&
		*sf.Hooks.DisablePostMerge
}

// PostMergeActive reports whether a post-merge hook should run after a landed
// merge: a non-empty [hooks].post-merge command that is not suppressed by
// [hooks].disable-post-merge. Uses the same emptiness test as RepairActive so
// a whitespace-only command is treated as unset.
func (sf Sweatfile) PostMergeActive() bool {
	if sf.PostMergeDisabled() {
		return false
	}
	cmd := sf.PostMergeHookCommand()
	return cmd != nil && stripEmptyLines(*cmd) != ""
}

// RepairHookCommand returns the [hooks].repair command, or nil when unset.
// When set (and not disabled via RepairDisabled), the merge runs it as a
// distinct REPAIR phase before the pre-merge VERIFY hook — auto-folding
// mechanical fixes into the merged commit. The canonical value is
// `conformist --commit --amend --exit-zero-on-fix`. See FDR 0018.
func (sf Sweatfile) RepairHookCommand() *string {
	if sf.Hooks == nil {
		return nil
	}
	return sf.Hooks.Repair
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

// SyspromptDocIndexDirs returns the configured [sysprompt].doc-index-dirs and
// whether it was set. When ok is false the caller applies its built-in default;
// when ok is true an empty slice means the design-record index is explicitly
// disabled. See FDR 0021.
func (sf Sweatfile) SyspromptDocIndexDirs() (dirs []string, ok bool) {
	if sf.Sysprompt == nil || sf.Sysprompt.DocIndexDirs == nil {
		return nil, false
	}
	return sf.Sysprompt.DocIndexDirs, true
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

// DisableMergeQueueEnabled reports whether [hooks].disable-merge-queue is
// true. When true, the per-repo merge serialization lock (issue #235) is
// disabled: merges fail immediately if the default branch moved during the
// pre-merge gate instead of queuing, rebasing, and re-gating under the lock.
func (sf Sweatfile) DisableMergeQueueEnabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.DisableMergeQueue != nil &&
		*sf.Hooks.DisableMergeQueue
}

// DisableMergeStackingEnabled reports whether [hooks].disable-merge-stacking is
// true. When true, the intra-session merge queue (spinclass#265) is disabled:
// a second merge-this-session-async while one is already running is refused
// ("a background job is already running") instead of enqueued to run when the
// current gate completes. The refusal still does not consume the pre-merge
// attestation (that fix — #265 deliverable 2 — is independent of stacking).
// This is the FDR 0025 rollback: distinct from disable-merge-queue, which
// governs the per-repo landing lock, not intra-session enqueue.
func (sf Sweatfile) DisableMergeStackingEnabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.DisableMergeStacking != nil &&
		*sf.Hooks.DisableMergeStacking
}

// RepairDisabled reports whether [hooks].disable-repair is true. It suppresses
// an inherited [hooks].repair command without having to clear the string,
// mirroring the disable-merge / disable-nix-gc opt-out shape. See FDR 0018.
func (sf Sweatfile) RepairDisabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.DisableRepair != nil &&
		*sf.Hooks.DisableRepair
}

// RepairActive reports whether a REPAIR phase should run for a merge: a
// non-empty [hooks].repair command that is not suppressed by
// [hooks].disable-repair. Uses the same emptiness test as the hook runner so
// a whitespace-only command is treated as unset.
func (sf Sweatfile) RepairActive() bool {
	if sf.RepairDisabled() {
		return false
	}
	cmd := sf.RepairHookCommand()
	return cmd != nil && stripEmptyLines(*cmd) != ""
}

// PreCommitHookCommand returns the [hooks].pre-commit command, or nil when
// unset. When set (and not disabled), `sc start` installs it as a per-session
// git pre-commit hook that repairs staged content at authoring time so each
// commit is conformant in history. The canonical value is
// `conformist --staged --exit-zero-on-fix`. See
// docs/plans/2026-06-16-per-commit-repair-hook-design.md.
func (sf Sweatfile) PreCommitHookCommand() *string {
	if sf.Hooks == nil {
		return nil
	}
	return sf.Hooks.PreCommit
}

// PreCommitDisabled reports whether [hooks].disable-pre-commit is true. It
// suppresses an inherited [hooks].pre-commit command without clearing the
// string, mirroring the disable-repair / disable-merge opt-out shape.
func (sf Sweatfile) PreCommitDisabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.DisablePreCommit != nil &&
		*sf.Hooks.DisablePreCommit
}

// PreCommitActive reports whether the per-session pre-commit hook should be
// installed: a non-empty [hooks].pre-commit command not suppressed by
// [hooks].disable-pre-commit. Uses the same emptiness test as RepairActive so a
// whitespace-only command is treated as unset.
func (sf Sweatfile) PreCommitActive() bool {
	if sf.PreCommitDisabled() {
		return false
	}
	cmd := sf.PreCommitHookCommand()
	return cmd != nil && stripEmptyLines(*cmd) != ""
}

// AutoRebuildOnResume reports whether [hooks].auto-rebuild-on-resume is true.
// When set, `sc resume` re-applies a stale worktree's setup (worktree.Reapply)
// before attaching, instead of only warning. Default false keeps resume
// side-effect-free. See the rebuild/staleness design.
func (sf Sweatfile) AutoRebuildOnResume() bool {
	return sf.Hooks != nil &&
		sf.Hooks.AutoRebuildOnResume != nil &&
		*sf.Hooks.AutoRebuildOnResume
}

// AllowStaleBase reports whether [hooks].allow-stale-base is true. When set,
// session creation proceeds even though the default branch could not be
// confirmed current — an unreachable remote, a dirty checkout blocking the
// fast-forward, a diverged local default (spinclass#250, internal/basebranch).
//
// This is the persistent half of the override; `sc start --allow-stale-base` is
// the per-invocation half. There is deliberately no equivalent MCP tool
// parameter: a spawned agent must not be able to wave away its own stale
// toolchain, so opting out stays a decision the repo's owner records here.
func (sf Sweatfile) AllowStaleBase() bool {
	return sf.Hooks != nil &&
		sf.Hooks.AllowStaleBase != nil &&
		*sf.Hooks.AllowStaleBase
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

// WorktreePathRewriteDisabled reports whether [hooks].disable-worktree-path-rewrite
// is true. When false (the default), the PreToolUse hook rewrites a tool-call path
// that targets the parent checkout into the active worktree; when true the rewrite
// is off (the legacy disallow-main-worktree deny, if enabled, then applies). No-op
// for implicit (main-checkout) sessions. See spinclass-sweatfile(5).
func (sf Sweatfile) WorktreePathRewriteDisabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.DisableWorktreePathRewrite != nil &&
		*sf.Hooks.DisableWorktreePathRewrite
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

// SessionSpawnEntry returns the detached-harness argv that `sc spawn`
// execs DIRECTLY (FDR-0017 Piece 1: spinclass no longer wraps in
// a multiplexer — the harness self-detaches and returns promptly, e.g. clown's
// --clown-attach=spawn). Defaults to the clown spawn form; override per the
// sweatfile cascade for a different harness. {prompt} = the brief, {dir} = the
// worktree.
func (sf Sweatfile) SessionSpawnEntry() []string {
	if sf.SessionEntry != nil && len(sf.SessionEntry.SpawnEntry) > 0 {
		return sf.SessionEntry.SpawnEntry
	}
	return []string{"clown", "--clown-attach=spawn", "--", "{prompt}"}
}

// SessionSpawnWindow returns the spawn-window argv template, or nil when
// unconfigured — there is no default: opening windows is a desktop
// preference (#149). See FDR 0006.
func (sf Sweatfile) SessionSpawnWindow() []string {
	if sf.SessionEntry != nil && len(sf.SessionEntry.SpawnWindow) > 0 {
		return sf.SessionEntry.SpawnWindow
	}
	return nil
}

// SessionModelFlags returns the configured [session-entry.model-flags]
// provider->flag map, falling back to the built-in default
// {"claude": "--model"} when unset or empty. See the spawn model-selection
// design doc.
func (sf Sweatfile) SessionModelFlags() map[string]string {
	if sf.SessionEntry != nil && len(sf.SessionEntry.ModelFlags) > 0 {
		return sf.SessionEntry.ModelFlags
	}
	return map[string]string{"claude": "--model"}
}

// SessionEnv returns the user-configured environment variables to inject
// into the session's process environment. These are exposed to
// `[session-entry].start`/`resume` argv expansion, to lifecycle hooks,
// and to the liveness probe. spinclass-owned vars (SPINCLASS_SESSION_ID,
// SPINCLASS_REPO, SPINCLASS_BRANCH, SPINCLASS_WORKTREE,
// SPINCLASS_DESCRIPTION, TMPDIR, CLAUDE_CODE_TMPDIR) are applied AFTER
// this map and so cannot be clobbered by user config.
//
// Typical use: set $SPINCLASS_GROUP so a multiplexer (posh under the
// FDR-0017 cutover) and any session-aware argv can reference the session's
// group symbolically. Returns nil when no [session-entry].env is configured.
func (sf Sweatfile) SessionEnv() map[string]string {
	if sf.SessionEntry == nil {
		return nil
	}
	return sf.SessionEntry.Env
}

// SessionLivenessProbe returns the configured [session-entry].liveness-probe
// argv.
//
// Deprecated: post-FDR-0017 clown owns the multiplexer attach and names
// sessions by its own per-instance key, so a `posh -g … list | grep
// $SPINCLASS_SESSION_ID`-style probe can never match. Liveness now derives from
// clown's presence index (internal/clown.PresenceByDecoration). No production
// code consults this accessor; it (and the `liveness-probe` field) are retained
// only until the eng home/spinclass.nix probe is dropped, then removed. See #201.
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
