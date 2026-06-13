package hooks

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/amarbel-llc/spinclass/internal/chat"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sessionlog"
	"github.com/amarbel-llc/spinclass/internal/sweatfileio"
	"github.com/amarbel-llc/spinclass/internal/worktree"
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

func Run(r io.Reader, w io.Writer, mainRepoRoot, sessionWorktree string, disallowMainWorktree bool) error {
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
		return runPreToolUse(input, w, mainRepoRoot, sessionWorktree, disallowMainWorktree)
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
	// Cheapest check (a single Lstat) — runs first.
	if worktree.IsWorktree(cwd) {
		maybeSendSpawnHello(cwd) // spawn handshake (FDR 0006); never blocks startup
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
func maybeSendSpawnHello(cwd string) {
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
	if err := chat.SendHello(st.SessionKey, st.SpawnedBy); err != nil {
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

	// Command failed -> write output to sentinel and block
	os.WriteFile(sentinelPath, output, 0o644)

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
	sessionJobStatusToolName      = "mcp__plugin_spinclass_spinclass__session-job-status"
	sessionJobCancelToolName      = "mcp__plugin_spinclass_spinclass__session-job-cancel"
	sessionJobWaitToolName        = "mcp__plugin_spinclass_spinclass__session-job-wait"
	nothingButTheTruthToolName    = "mcp__plugin_spinclass_spinclass__nothing-but-the-truth"
	listToolName                  = "mcp__plugin_spinclass_spinclass__list"
	updateDescriptionToolName     = "mcp__plugin_spinclass_spinclass__update-this-session-description"
	chatSendToolName              = "mcp__plugin_spinclass_spinclass__chat-send"
	chatReadToolName              = "mcp__plugin_spinclass_spinclass__chat-read"
	chatListSessionsToolName      = "mcp__plugin_spinclass_spinclass__chat-list-sessions"
	validateToolName              = "mcp__plugin_spinclass_spinclass__validate"
)

func runPreToolUse(input hookInput, w io.Writer, mainRepoRoot, sessionWorktree string, disallowMainWorktree bool) error {
	if input.AgentID != "" {
		switch input.ToolName {
		case mergeThisSessionToolName, checkThisSessionToolName,
			mergeThisSessionAsyncToolName, checkThisSessionAsyncToolName,
			nothingButTheTruthToolName:
			return writeDeny(w, "merge and attestation tools are not available to subagents; only the main agent may call them")
		}
	}

	switch input.ToolName {
	case listToolName, updateDescriptionToolName, validateToolName,
		sessionJobStatusToolName, sessionJobCancelToolName, sessionJobWaitToolName:
		// Benign, session-scoped spinclass tools: list, validate,
		// session-job-status, and session-job-wait are read-only (wait only
		// blocks on an existing job); update-this-session-description and
		// session-job-cancel only mutate spinclass's own session/job metadata.
		// Auto-approve unconditionally so agents never get a permission prompt
		// for them.
		return writeAllow(w, "spinclass session-management tool, safe to auto-approve")
	case chatSendToolName, chatReadToolName, chatListSessionsToolName:
		// The cross-session chat tools are benign (no repo or filesystem
		// mutation): send posts a message, read only advances spinclass's
		// own read cursor, list-sessions is read-only. Auto-approve so
		// inter-session coordination never trips a permission prompt.
		return writeAllow(w, "spinclass cross-session chat tool is benign, safe to auto-approve")
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
	defer f.Close()

	data, err := json.Marshal(input)
	if err != nil {
		return nil
	}

	data = append(data, '\n')
	f.Write(data)

	return nil
}
