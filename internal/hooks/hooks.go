package hooks

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/perms"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sessionlog"
	"code.linenisgreat.com/spinclass/internal/spawnhandshake"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/worktree"
	"github.com/google/shlex"
)

type hookInput struct {
	HookEventName string         `json:"hook_event_name"`
	SessionID     string         `json:"session_id"`
	AgentID       string         `json:"agent_id"`
	ToolName      string         `json:"tool_name"`
	ToolInput     map[string]any `json:"tool_input"`
	CWD           string         `json:"cwd"`
}

// RunOption configures optional Run behavior.
type RunOption func(*runOptions)

type runOptions struct {
	rewriteEnabled bool
}

// WithWorktreePathRewrite toggles the #176 parent-checkout → worktree path
// rewrite. It is OFF unless an option enables it, so the default-on policy lives
// in Handle (the production entry) while Run's own tests opt in explicitly.
func WithWorktreePathRewrite(enabled bool) RunOption {
	return func(o *runOptions) { o.rewriteEnabled = enabled }
}

func Run(r io.Reader, w io.Writer, mainRepoRoot, sessionWorktree string, disallowMainWorktree bool, opts ...RunOption) error {
	var o runOptions
	for _, fn := range opts {
		fn(&o)
	}

	var input hookInput
	if err := json.NewDecoder(r).Decode(&input); err != nil {
		return fmt.Errorf("decoding hook input: %w", err)
	}

	switch input.HookEventName {
	case "Stop":
		return runStopHook(input, w)
	case "PostToolUse":
		return runPostToolUseLog(input)
	case "SessionStart":
		return runSessionStart(input)
	case "SessionEnd":
		return runSessionEnd(input)
	default:
		return runPreToolUse(input, w, mainRepoRoot, sessionWorktree, disallowMainWorktree, o.rewriteEnabled)
	}
}

// implicitRand derives the per-session suffix from the Claude session id:
// sha256(sessionID)[:8] as hex. Stable across the session's lifetime, so
// SessionStart and SessionEnd derive the same value.
func implicitRand(sessionID string) string {
	h := sha256.Sum256([]byte(sessionID))
	return fmt.Sprintf("%x", h[:8])
}

// runSessionStart materializes an implicit session when cwd is a deliberate
// main checkout (a git repo whose .git is a directory, NOT a .worktrees
// worktree) — on ANY branch, not just the default. All failures are swallowed
// (return nil) — a hook must never block session startup. Honors
// [hooks].disable-implicit-sessions.
func runSessionStart(input hookInput) error {
	cwd := input.CWD
	if cwd == "" || input.SessionID == "" {
		return nil
	}
	// Gate 1: not inside an sc-created worktree (those already have state).
	// Cheapest check (a single Lstat) — runs first. input.SessionID is the
	// worker's claude --session-id, which by construction equals its posh
	// multiplexer session name (clown mints one UUID for both) — the driver
	// needs it to reattach, so it rides along in the hello (direction B).
	if worktree.IsWorktree(cwd) {
		maybeSendSpawnHello(cwd, input.SessionID) // spawn handshake (FDR 0006); never blocks startup
		return nil
	}
	// os.Getppid() is best-effort: the hook handler is a short-lived
	// subprocess whose parent may be the Claude process or a transient
	// shell wrapper — not empirically verified. PID-liveness is only a
	// backstop reaper; SessionEnd delete + SessionStart sweep are primary.
	_, _ = MaterializeImplicit(cwd, implicitRand(input.SessionID), os.Getppid())
	return nil
}

// MaterializeImplicit applies the main-checkout gates and, when they pass,
// writes an implicit session state file keyed by randID with pid as the
// liveness PID, returning the session key and true. It is the shared core
// behind the SessionStart hook and the serve process's lazy chat/spawn sender
// resolution (#141) — keeping the gates in one place guarantees the two paths
// can never disagree about what qualifies as a materializable checkout.
// Callers must have already established cwd is NOT an sc worktree.
//
// Returns ("", false) when gated or on any failure; errors are swallowed and
// logged — both callers treat materialization as best-effort.
func MaterializeImplicit(cwd, randID string, pid int) (string, bool) {
	// Gate: a git repo whose checkout root == cwd. The caller's not-a-worktree
	// check already proved .git is a directory (main checkout), not a
	// file/symlink (worktree), so the main checkout on ANY branch qualifies —
	// we do NOT restrict to the default branch. Bail silently on any error.
	// This cheap git-repo discriminator runs BEFORE the sweatfile I/O walk
	// (the knob gate) so the common non-git-dir case (e.g. ~/Downloads)
	// skips the hierarchy stat-walk entirely.
	repoRoot, err := gitToplevel(cwd)
	// gitToplevel (`git rev-parse --show-toplevel`) canonicalizes symlinks; the
	// raw hook cwd does not. A checkout under a symlinked path (symlinked
	// $HOME/TMPDIR, macOS /var -> /private/var) would otherwise fail this gate —
	// filepath.Clean normalizes . / .. / separators but never resolves symlinks,
	// so the two sides disagree. Resolve BOTH sides before comparing so the
	// checkout still qualifies as its own toplevel. resolvePath degrades to
	// filepath.Clean when EvalSymlinks fails (e.g. a vanished path), matching the
	// surrounding path handling.
	if err != nil || resolvePath(cwd) != resolvePath(repoRoot) {
		return "", false
	}
	branch, err := git.BranchCurrent(cwd)
	if err != nil || branch == "" { // empty = detached HEAD: no branch hint
		return "", false
	}
	// Rollback knob. Now that we know this is a materializable main checkout,
	// the sweatfile hierarchy walk only runs when it matters, and stays
	// BEFORE the sweep + write so a disabled session writes nothing.
	if home, _ := os.UserHomeDir(); home != "" {
		if res, err := sweatfileio.LoadHierarchy(home, cwd); err == nil &&
			res.Merged.DisableImplicitSessionsEnabled() {
			return "", false
		}
	}

	// Orphan sweep before our own write (backstop for missed SessionEnd).
	if err := session.SweepDeadImplicit(cwd); err != nil {
		sessionlog.Errorf("MaterializeImplicit SweepDeadImplicit-failed checkout=%s err=%v", cwd, err)
	}

	// Key is <repo>/<rand> — the branch is deliberately NOT part of the
	// identity. This keeps the key stable across a mid-session branch switch
	// and avoids slash-bearing branch names (e.g. feature/wip) leaking a
	// second "/" into the key. Branch is captured below as a display-only
	// hint and refreshed on every re-fire (see Branch field).
	key := filepath.Base(repoRoot) + "/" + randID
	s := session.State{
		Kind:         session.KindImplicit,
		PID:          pid,
		SessionState: session.StateActive,
		RepoPath:     repoRoot,
		WorktreePath: cwd,
		// Branch is a display hint, not part of the key. SessionStart re-fires
		// (startup/resume/clear/compact) re-run git.BranchCurrent and WriteImplicit
		// upserts the same state-<rand>.json, so the hint tracks the checkout's
		// current branch live while the key stays put.
		Branch:     branch,
		SessionKey: key,
		StartedAt:  time.Now(),
		Env:        map[string]string{"SPINCLASS_SESSION_ID": key},
	}
	if err := session.WriteImplicit(s, randID); err != nil {
		return "", false
	}
	return key, true
}

// maybeSendSpawnHello emits the spawn handshake (FDR 0006) when cwd is an sc
// worktree whose session state carries SpawnedBy: it sends the hello to the
// driver, then adopts the state (PID, active, HelloSentAt dedup marker so
// resume/clear/compact re-fires do not re-hello). The hello uses the state's
// SessionKey VERBATIM — the driver's WaitForHello filters From == that exact
// key, so recomputing it from git here would break the gate. Order matters:
// send first (a state-write failure must not suppress the handshake), mark
// only on success (a send failure must not set HelloSentAt). All failures are
// swallowed and logged via sessionlog — a hook must never block startup.
func maybeSendSpawnHello(cwd, poshSessionID string) {
	repoPath, err := git.CommonDir(cwd)
	if err != nil {
		sessionlog.Errorf("maybeSendSpawnHello common-dir-failed cwd=%s err=%v", cwd, err)
		return
	}
	branch, err := git.BranchCurrent(cwd)
	if err != nil || branch == "" { // empty = detached HEAD: no session key to read
		return
	}
	st, err := session.Read(repoPath, branch)
	if err != nil {
		// No session state — a worktree spinclass doesn't track. Expected
		// for non-sc worktrees; stay silent.
		return
	}
	if st.SpawnedBy == "" || st.HelloSentAt != nil {
		return
	}
	if err := spawnhandshake.SendHello(st.SessionKey, st.SpawnedBy, poshSessionID); err != nil {
		sessionlog.Errorf("maybeSendSpawnHello send-failed key=%s to=%s err=%v", st.SessionKey, st.SpawnedBy, err)
		return
	}
	now := time.Now().UTC()
	st.HelloSentAt = &now
	// os.Getppid() is best-effort, same caveat as the implicit path: the
	// hook handler is a short-lived subprocess whose parent may be the
	// Claude process or a transient shell wrapper.
	st.PID = os.Getppid()
	st.SessionState = session.StateActive
	if err := session.Write(*st); err != nil {
		sessionlog.Errorf("maybeSendSpawnHello write-failed key=%s err=%v", st.SessionKey, err)
	}
}

// runSessionEnd hard-deletes the implicit session for this session_id. Misses
// (crash, kill -9, or the SessionEnd timeout) are backstopped by
// SweepDeadImplicit on the next SessionStart. Swallows errors — a hook must
// never block session teardown.
func runSessionEnd(input hookInput) error {
	if input.CWD == "" || input.SessionID == "" {
		return nil
	}
	if err := session.RemoveImplicit(input.CWD, implicitRand(input.SessionID)); err != nil {
		sessionlog.Errorf("runSessionEnd RemoveImplicit-failed checkout=%s err=%v", input.CWD, err)
	}
	return nil
}

func runStopHook(input hookInput, w io.Writer) error {
	tmpDir := os.TempDir()
	sentinelPath := filepath.Join(tmpDir, "stop-hook-"+input.SessionID)

	if _, err := os.Stat(sentinelPath); err == nil {
		return nil // second invocation -> approve
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil // can't load sweatfile -> approve
	}

	result, err := sweatfileio.LoadHierarchy(home, input.CWD)
	stopCmd := result.Merged.StopHookCommand()
	if err != nil || stopCmd == nil || *stopCmd == "" {
		return nil // no stop hook configured -> approve
	}

	cmd := exec.Command("sh", "-c", *stopCmd)
	cmd.Dir = input.CWD
	output, cmdErr := cmd.CombinedOutput()

	if cmdErr == nil {
		return nil // command passed -> approve
	}

	// Command failed -> write output to sentinel and block (best-effort)
	_ = os.WriteFile(sentinelPath, output, 0o644)

	reason := fmt.Sprintf("stop hook failed: %s", *stopCmd)
	systemMsg := fmt.Sprintf(
		"Stop hook failed. Output written to %s. Review the failures and address them before completing.",
		sentinelPath,
	)

	decision := map[string]any{
		"decision":      "block",
		"reason":        reason,
		"systemMessage": systemMsg,
	}

	return json.NewEncoder(w).Encode(decision)
}

// Spinclass ships as a Claude Code plugin named "spinclass" with an MCP
// server also named "spinclass", so its tools appear to Claude Code as
// mcp__plugin_spinclass_spinclass__<tool>.
const (
	mergeThisSessionToolName      = "mcp__plugin_spinclass_spinclass__merge-this-session"
	checkThisSessionToolName      = "mcp__plugin_spinclass_spinclass__check-this-session"
	mergeThisSessionAsyncToolName = "mcp__plugin_spinclass_spinclass__merge-this-session-async"
	checkThisSessionAsyncToolName = "mcp__plugin_spinclass_spinclass__check-this-session-async"
	sessionJobCancelToolName      = "mcp__plugin_spinclass_spinclass__session-job-cancel"
	nothingButTheTruthToolName    = "mcp__plugin_spinclass_spinclass__nothing-but-the-truth"
	listToolName                  = "mcp__plugin_spinclass_spinclass__list"
	updateDescriptionToolName     = "mcp__plugin_spinclass_spinclass__update-this-session-description"
	validateToolName              = "mcp__plugin_spinclass_spinclass__validate"
	spawnSessionToolName          = "mcp__plugin_spinclass_spinclass__spawn-session"
	forkSessionToolName           = "mcp__plugin_spinclass_spinclass__fork-session"
	closeChildSessionToolName     = "mcp__plugin_spinclass_spinclass__close-child-session"
)

func runPreToolUse(input hookInput, w io.Writer, mainRepoRoot, sessionWorktree string, disallowMainWorktree, rewriteEnabled bool) error {
	if input.AgentID != "" {
		switch input.ToolName {
		case mergeThisSessionToolName, checkThisSessionToolName,
			mergeThisSessionAsyncToolName, checkThisSessionAsyncToolName,
			nothingButTheTruthToolName:
			return writeDeny(w, "merge and attestation tools are not available to subagents; only the main agent may call them")
		}
	}

	// The always-ask floor, shared verbatim with the perms-tier surface
	// (perms.AlwaysAsk is the single source of truth for both). An `ask`
	// decision overrides every allow-list — claude-allow, perms tier,
	// permissive mode — so no spinclass-reachable configuration can make these
	// run silently (#151). Checked before the auto-approve switch below so a
	// dangerous invocation of an otherwise-benign tool still prompts.
	// The always-ask floor, shared verbatim with the perms-tier surface
	// (perms.AlwaysAsk is the single source of truth for both). An `ask`
	// decision overrides every allow-list — claude-allow, perms tier,
	// permissive mode — so no spinclass-reachable configuration can make these
	// run silently (#151).
	//
	// This MUST stay ahead of the auto-approve switch below, which now lists
	// close-child-session: reversing the two lets a forcing reap match the
	// benign case and return `allow` before the floor is ever consulted
	// (verified — the ordering is load-bearing, not stylistic).
	if reason, ask := perms.AlwaysAsk(input.ToolName, input.ToolInput); ask {
		return writeAsk(w, reason)
	}

	switch input.ToolName {
	case listToolName, updateDescriptionToolName, validateToolName,
		sessionJobCancelToolName, closeChildSessionToolName:
		// Benign, session-scoped spinclass tools: list and validate are
		// read-only; update-this-session-description and session-job-cancel only
		// mutate spinclass's own session/job metadata.
		// close-child-session reaches here only WITHOUT force (the check above
		// caught the forcing case), where close.RunResolved still refuses a
		// child holding unmerged work and the caller is already restricted to
		// workers it spawned — so the most it can do is remove a clean,
		// fully-integrated worktree.
		// Auto-approve unconditionally so agents never get a permission prompt
		// for them.
		return writeAllow(w, "spinclass session-management tool, safe to auto-approve")
	case mergeThisSessionToolName, mergeThisSessionAsyncToolName:
		if hasPreMergeHook(input.CWD) {
			return writeAllow(w, "sweatfile [hooks].pre-merge gates this merge")
		}
	case checkThisSessionToolName, checkThisSessionAsyncToolName:
		if hasPreMergeHook(input.CWD) {
			return writeAllow(w, "sweatfile [hooks].pre-merge is the agent-CI surface")
		}
	case nothingButTheTruthToolName:
		if hasPreMergeSkills(input.CWD) {
			return writeAllow(w, "sweatfile [[pre-merge-skills]] makes this tool the only path to a merge")
		}
	}

	resolvedMain := resolvePath(mainRepoRoot)
	resolvedWorktree := resolvePath(sessionWorktree)

	// Always guard session-config paths, even when disallow-main-worktree is off.
	if input.ToolName == "Read" || input.ToolName == "Write" || input.ToolName == "Edit" {
		if fp, ok := input.ToolInput["file_path"].(string); ok && fp != "" {
			if isInsideSpinclassDir(fp, resolvedMain, resolvedWorktree) {
				return writeDeny(w, fmt.Sprintf(
					"Path %s is inside the .spinclass directory, which is managed by spinclass.", fp,
				))
			}
			if (input.ToolName == "Write" || input.ToolName == "Edit") && isSweatfile(fp, resolvedMain, resolvedWorktree) {
				return writeAsk(w, fmt.Sprintf(
					"This modifies the sweatfile at %s, which controls session configuration.", fp,
				))
			}
		}
	}

	// #176: rewrite a parent-checkout path into the session worktree. Runs after
	// the session-config guards (which win for .spinclass/sweatfile) and before
	// the opt-in disallow-main-worktree deny, so an enabled rewrite supersedes the
	// deny for any path it corrects. The default-on policy is resolved in Handle;
	// no-op for implicit sessions (resolvedMain == "").
	if rewriteEnabled && mainRepoRoot != "" && sessionWorktree != "" {
		if updated, summary, changed := rewriteWorktreePaths(input, resolvedMain, resolvedWorktree); changed {
			reason := fmt.Sprintf("redirected into the session worktree (%s): %s", resolvedWorktree, summary)
			return writeRewrite(w, updated, reason, "[spinclass] "+reason)
		}
	}

	if !disallowMainWorktree || mainRepoRoot == "" {
		return nil
	}

	// Check for "cd <main-worktree> && <cmd>" pattern in Bash commands.
	if input.ToolName == "Bash" {
		if reason := checkBashCdToMainWorktree(input, resolvedMain, resolvedWorktree); reason != "" {
			return writeDeny(w, reason)
		}
	}

	paths := extractPaths(input)
	if paths == nil {
		return nil
	}

	for _, p := range paths {
		if isInsideMainWorktree(p, resolvedMain, resolvedWorktree) {
			return writeDeny(w, fmt.Sprintf(
				"Path %s is in the main worktree (%s). Restrict operations to the session worktree (%s).",
				p, resolvedMain, resolvedWorktree,
			))
		}
	}

	return nil
}

func writeAsk(w io.Writer, reason string) error {
	output := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "ask",
			"permissionDecisionReason": reason,
		},
	}
	return json.NewEncoder(w).Encode(output)
}

func writeAllow(w io.Writer, reason string) error {
	output := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "allow",
			"permissionDecisionReason": reason,
		},
	}
	return json.NewEncoder(w).Encode(output)
}

func hasPreMergeHook(cwd string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	result, err := sweatfileio.LoadHierarchy(home, cwd)
	if err != nil {
		return false
	}
	cmd := result.Merged.PreMergeHookCommand()
	return cmd != nil && *cmd != ""
}

func hasPreMergeSkills(cwd string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	result, err := sweatfileio.LoadHierarchy(home, cwd)
	if err != nil {
		return false
	}
	return len(result.Merged.ActivePreMergeSkills()) > 0
}

func isInsideSpinclassDir(path, mainRepoRoot, sessionWorktree string) bool {
	resolved := resolvePath(path)
	for _, root := range []string{mainRepoRoot, sessionWorktree} {
		if root == "" {
			continue
		}
		dir := filepath.Join(root, ".spinclass") + string(filepath.Separator)
		if strings.HasPrefix(resolved, dir) || resolved == filepath.Join(root, ".spinclass") {
			return true
		}
	}
	return false
}

func isSweatfile(path, mainRepoRoot, sessionWorktree string) bool {
	resolved := resolvePath(path)
	if mainRepoRoot != "" && resolved == filepath.Join(mainRepoRoot, "sweatfile") {
		return true
	}
	if sessionWorktree != "" && resolved == filepath.Join(sessionWorktree, "sweatfile") {
		return true
	}
	return false
}

func writeDeny(w io.Writer, reason string) error {
	output := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	}
	return json.NewEncoder(w).Encode(output)
}

// writeRewrite emits a PreToolUse "allow" decision whose updatedInput replaces the
// tool's entire input with the path-corrected version (#176). The nested
// hookSpecificOutput form (with updatedInput) is the shape claude-code honors for
// all tools incl. MCP plugin tools; systemMessage surfaces the correction so the
// rewrite is not silent.
func writeRewrite(w io.Writer, updatedInput map[string]any, reason, sysMsg string) error {
	output := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "allow",
			"permissionDecisionReason": reason,
			"updatedInput":             updatedInput,
		},
		"systemMessage": sysMsg,
	}
	return json.NewEncoder(w).Encode(output)
}

// builtinRewriteTools are the built-in Claude Code tools whose path arguments the
// worktree path rewrite (#176) covers. Beyond these, the rewrite applies to every
// moxy tool (the mcp__plugin_moxy_moxy__ prefix) — see isRewriteTargetTool.
var builtinRewriteTools = map[string]bool{
	"Read": true, "Write": true, "Edit": true, "NotebookEdit": true,
	"Glob": true, "Grep": true, "Bash": true,
}

// pathArgKeys are the tool-input argument names that carry a filesystem path the
// rewrite considers (scalar string or []string). Keyed by argument name rather
// than per-tool so the same set transparently covers the built-in tools and all
// moxy path tools (folio_*, rg_search, arboretum_*, grit_* repo_path, get-hubbed
// copy-* dest_path/dest_dir, hamster cwd, jq files, …). Only values that resolve
// to an absolute path under the parent checkout are rewritten, so a relative or
// non-path value under one of these keys (e.g. an in-repo get-hubbed path) is
// left untouched. Bash's "command" is handled separately (tokenized).
var pathArgKeys = map[string]bool{
	"file_path": true, "path": true, "notebook_path": true,
	"dest_path": true, "dest_dir": true, "source": true, "destination": true,
	"target": true, "repo_path": true, "cwd": true, "flake_dir": true,
	"file": true, "files": true, "paths": true,
}

// isRewriteTargetTool reports whether a tool participates in the path rewrite: the
// built-in file tools plus every moxy tool. Other MCP servers' tools are left
// alone (their path semantics are theirs to define).
func isRewriteTargetTool(name string) bool {
	return builtinRewriteTools[name] || strings.HasPrefix(name, "mcp__plugin_moxy_moxy__")
}

// rewritePathString rewrites a single path value that targets the parent checkout
// into the session worktree, returning (newValue, changed). It leaves the value
// unchanged when it is already inside the worktree, under the parent checkout's
// .worktrees/ (this or a sibling worktree) or .spinclass/ trees, or outside the
// parent checkout entirely. mainRoot and worktree must already be symlink-resolved.
func rewritePathString(value, mainRoot, worktree string) (string, bool) {
	if value == "" {
		return value, false
	}
	resolved := resolvePath(value)
	sep := string(filepath.Separator)
	// Already inside the session worktree — nothing to do. The worktree is itself
	// under mainRoot, so this MUST be checked before the under-mainRoot test.
	if resolved == worktree || strings.HasPrefix(resolved, worktree+sep) {
		return value, false
	}
	// Outside the parent checkout entirely ($HOME, /tmp, sibling repos, /nix/store).
	if resolved != mainRoot && !strings.HasPrefix(resolved, mainRoot+sep) {
		return value, false
	}
	rel, err := filepath.Rel(mainRoot, resolved)
	if err != nil {
		return value, false
	}
	if rel != "." {
		first := rel
		if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
			first = rel[:i]
		}
		// Worktree-space (this/other worktrees) and spinclass-managed dirs are not
		// parent-checkout files; leave them (the existing guards handle .spinclass).
		if first == ".worktrees" || first == ".spinclass" {
			return value, false
		}
	}
	return filepath.Join(worktree, rel), true
}

// rewriteArgValue rewrites a scalar string path value or each string element of a
// []any list value, returning the (possibly new) value and whether anything changed.
func rewriteArgValue(v any, mainRoot, worktree string) (any, bool) {
	switch x := v.(type) {
	case string:
		return rewritePathString(x, mainRoot, worktree)
	case []any:
		changed := false
		out := make([]any, len(x))
		for i, e := range x {
			if s, ok := e.(string); ok {
				nv, c := rewritePathString(s, mainRoot, worktree)
				out[i] = nv
				changed = changed || c
			} else {
				out[i] = e
			}
		}
		return out, changed
	}
	return v, false
}

// rewriteBashCommand rewrites parent-checkout absolute paths embedded in a Bash
// command into the session worktree. It tokenizes (shlex) to find absolute-path
// tokens, rewrites each via rewritePathString (so the same exemptions apply), and
// substitutes the token text in the raw command — which also corrects a quoted
// occurrence, since the unquoted token is a substring of the quoted form.
func rewriteBashCommand(cmd, mainRoot, worktree string) (string, bool) {
	tokens, err := shlex.Split(cmd)
	if err != nil {
		return cmd, false
	}
	changed := false
	seen := map[string]bool{}
	for _, tok := range tokens {
		if !strings.HasPrefix(tok, "/") || seen[tok] {
			continue
		}
		seen[tok] = true
		if nv, c := rewritePathString(tok, mainRoot, worktree); c {
			cmd = strings.ReplaceAll(cmd, tok, nv)
			changed = true
		}
	}
	return cmd, changed
}

// rewriteWorktreePaths returns a path-corrected copy of the tool input, a summary
// of the changed argument keys, and whether any rewrite happened. It returns
// (nil, "", false) when the tool is out of scope or nothing needed correcting.
func rewriteWorktreePaths(input hookInput, mainRoot, worktree string) (map[string]any, string, bool) {
	if !isRewriteTargetTool(input.ToolName) || len(input.ToolInput) == 0 {
		return nil, "", false
	}
	updated := make(map[string]any, len(input.ToolInput))
	for k, v := range input.ToolInput {
		updated[k] = v
	}
	var changed []string
	if input.ToolName == "Bash" {
		if cmd, ok := updated["command"].(string); ok {
			if nv, c := rewriteBashCommand(cmd, mainRoot, worktree); c {
				updated["command"] = nv
				changed = append(changed, "command")
			}
		}
	} else {
		for key := range pathArgKeys {
			v, ok := updated[key]
			if !ok {
				continue
			}
			if nv, c := rewriteArgValue(v, mainRoot, worktree); c {
				updated[key] = nv
				changed = append(changed, key)
			}
		}
	}
	if len(changed) == 0 {
		return nil, "", false
	}
	sort.Strings(changed)
	return updated, strings.Join(changed, ", "), true
}

// checkBashCdToMainWorktree detects commands like "cd /path/to/main/repo && just"
// where the cd target is inside the main worktree. Returns a deny reason with
// the corrected command, or empty string if no match.
func checkBashCdToMainWorktree(input hookInput, mainRepoRoot, sessionWorktree string) string {
	cmd, ok := input.ToolInput["command"].(string)
	if !ok || cmd == "" {
		return ""
	}

	tokens, err := shlex.Split(cmd)
	if err != nil || len(tokens) < 2 || tokens[0] != "cd" {
		return ""
	}

	cdTarget := tokens[1]

	// Find separator (&&, ;) and extract the rest of the command.
	var restTokens []string
	for i := 2; i < len(tokens); i++ {
		if tokens[i] == "&&" || tokens[i] == ";" {
			restTokens = tokens[i+1:]
			break
		}
	}

	resolved := resolvePath(cdTarget)
	if !isInsideMainWorktree(resolved, mainRepoRoot, sessionWorktree) {
		return ""
	}

	suggestion := strings.Join(restTokens, " ")
	if suggestion == "" {
		suggestion = "(no command after cd)"
	}

	return fmt.Sprintf(
		"Command changes directory to main worktree (%s). You are already in the session worktree (%s). Use: %s",
		cdTarget, sessionWorktree, suggestion,
	)
}

func extractPaths(input hookInput) []string {
	switch input.ToolName {
	case "Read", "Write", "Edit":
		if fp, ok := input.ToolInput["file_path"].(string); ok && fp != "" {
			return []string{fp}
		}
	case "Glob", "Grep", "Find":
		if p, ok := input.ToolInput["path"].(string); ok && p != "" {
			return []string{p}
		}
	case "Bash":
		return extractAbsolutePathsFromCommand(input)
	case "Task":
		return nil
	}
	return nil
}

func extractAbsolutePathsFromCommand(input hookInput) []string {
	cmd, ok := input.ToolInput["command"].(string)
	if !ok || cmd == "" {
		return nil
	}

	tokens, err := shlex.Split(cmd)
	if err != nil {
		return nil
	}

	var paths []string
	for _, token := range tokens {
		if strings.HasPrefix(token, "/") {
			paths = append(paths, token)
		}
	}
	return paths
}

func resolvePath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved
	}

	// Path doesn't fully exist — walk up until we find an existing ancestor,
	// resolve symlinks there, then re-append the non-existent suffix.
	cleaned := filepath.Clean(path)
	var suffix []string
	current := cleaned
	for {
		parent := filepath.Dir(current)
		suffix = append(suffix, filepath.Base(current))
		if parent == current {
			break
		}
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			// Reverse suffix and join.
			for i, j := 0, len(suffix)-1; i < j; i, j = i+1, j-1 {
				suffix[i], suffix[j] = suffix[j], suffix[i]
			}
			return filepath.Join(append([]string{resolved}, suffix...)...)
		}
		current = parent
	}

	return cleaned
}

func isInsideMainWorktree(path, mainRepoRoot, sessionWorktree string) bool {
	resolved := resolvePath(path)

	if sessionWorktree != "" &&
		(resolved == sessionWorktree || strings.HasPrefix(resolved, sessionWorktree+string(filepath.Separator))) {
		return false
	}

	return resolved == mainRepoRoot || strings.HasPrefix(resolved, mainRepoRoot+string(filepath.Separator))
}

// logDir returns the XDG_LOG_HOME-based log directory for spinclass.
// Per amarbel-llc/xdg basedir spec: $XDG_LOG_HOME defaults to $HOME/.local/log.
func logDir() string {
	base := os.Getenv("XDG_LOG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "log")
	}
	return filepath.Join(base, "spinclass", "tool-uses")
}

// runPostToolUseLog appends the raw hook payload as a JSONL line to
// $XDG_LOG_HOME/spinclass/tool-uses/<session-key>.jsonl. Fails silently —
// a logging failure must never block Claude.
func runPostToolUseLog(input hookInput) error {
	session := os.Getenv("SPINCLASS_SESSION_ID")
	if session == "" {
		return nil
	}

	dir := logDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil // fail silently
	}

	// Sanitize session key for filename: "repo/branch" → "repo--branch"
	filename := strings.ReplaceAll(session, "/", "--") + ".jsonl"
	logPath := filepath.Join(dir, filename)

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil // fail silently
	}
	defer func() { _ = f.Close() }()

	data, err := json.Marshal(input)
	if err != nil {
		return nil
	}

	data = append(data, '\n')
	_, _ = f.Write(data)

	return nil
}
