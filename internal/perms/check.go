package perms

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Tool names the always-ask floor is expressed over.
const (
	spawnSessionTool      = "mcp__plugin_spinclass_spinclass__spawn-session"
	forkSessionTool       = "mcp__plugin_spinclass_spinclass__fork-session"
	closeChildSessionTool = "mcp__plugin_spinclass_spinclass__close-child-session"
)

// AlwaysAsk reports whether an invocation must prompt the human, and why. It is
// the single source of truth for spinclass's always-ask floor, consulted by BOTH
// enforcement surfaces: the PreToolUse hook turns a true into an `ask` decision
// (which overrides every allow-list), and RunCheck below refuses to auto-approve
// it from a perms tier. Duplicating the rule across the two would put a seam in
// a security floor, so they share this.
//
// Note this judges an INVOCATION, not a tool: it was a name-keyed map until
// close-child-session needed a decision that depends on an argument (#249).
// Callers must pass the tool input, not just the name.
func AlwaysAsk(toolName string, toolInput map[string]any) (string, bool) {
	switch toolName {
	case spawnSessionTool, forkSessionTool:
		// Spawning/forking launches a full harness-booted agent that
		// immediately consumes tokens — categorically heavier than any other
		// spinclass tool, and unconditional regardless of arguments.
		//
		// Since the #148 recursive-spawn guard was removed, this is the ONLY
		// thing preventing runaway fan-out: a worker may now spawn its own
		// workers, so an `ask` at every depth is what keeps a recursive tree
		// from growing without a human in the loop. See FDR 0006 / #151.
		return "spawning a worker session launches a token-consuming agent; confirm each invocation", true

	case closeChildSessionTool:
		// A reap without force is safe to run silently: close.RunResolved
		// refuses a child holding uncommitted changes or unintegrated commits,
		// and authorizeChildReap already limits the caller to workers it
		// spawned itself. So the worst case is deleting a clean, fully merged
		// worktree — nothing is lost.
		//
		// force is what removes that floor: it discards unmerged commits and
		// the worktree outright. Prompt on exactly that, so tidying up after a
		// finished worker stays frictionless while destruction stays a human
		// decision. A perms tier cannot draw this line itself — MatchingRule
		// discards tool input for MCP tools (BuildPermissionString returns the
		// bare name), so allow-listing the tool at all would otherwise grant
		// force too.
		if forceRequested(toolInput) {
			return "force discards the child's uncommitted changes and unmerged commits; confirm each invocation", true
		}
		return "", false
	}

	return "", false
}

// forceRequested reads the `force` argument, failing CLOSED: anything that is
// not definitively "no force" counts as a force request.
//
// The naive `toolInput["force"].(bool)` yields false for every non-boolean —
// the string "true", a number, anything unexpected — which would auto-approve
// precisely the invocation this floor exists to catch. Since the whole reason
// the rule is mirrored into RunCheck is to avoid trusting that another layer
// got it right, it should not then trust the payload's shape either.
func forceRequested(toolInput map[string]any) bool {
	v, present := toolInput["force"]
	if !present || v == nil {
		// Absent, or JSON null — both unambiguously mean "not set".
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

// RunCheck reads a PermissionRequest hook payload from r, checks if the tool
// invocation matches any curated tier rule, and writes an allow decision to w
// when matched. When no rule matches, nothing is written to w.
func RunCheck(r io.Reader, w io.Writer, tiersDir string) error {
	var input struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
		CWD       string         `json:"cwd"`
	}

	if err := json.NewDecoder(r).Decode(&input); err != nil {
		return fmt.Errorf("decoding hook input: %w", err)
	}

	if _, ask := AlwaysAsk(input.ToolName, input.ToolInput); ask {
		return nil // defer to the always-ask PreToolUse decision
	}

	repo := repoFromCWD(input.CWD)

	globalPath := filepath.Join(tiersDir, "global.json")
	globalTier, err := LoadTierFile(globalPath)
	if err != nil {
		return fmt.Errorf("loading global tier: %w", err)
	}

	var repoTier Tier
	if repo != "" {
		repoPath := filepath.Join(tiersDir, "repos", repo+".json")
		repoTier, err = LoadTierFile(repoPath)
		if err != nil {
			return fmt.Errorf("loading repo tier %s: %w", repo, err)
		}
	}

	tierName := ""

	if repo != "" {
		if _, ok := MatchingRule(repoTier.Allow, input.ToolName, input.ToolInput); ok {
			tierName = repo
		}
	}

	if tierName == "" {
		if _, ok := MatchingRule(globalTier.Allow, input.ToolName, input.ToolInput); ok {
			tierName = "global"
		}
	}

	if tierName == "" {
		return nil
	}

	permStr := BuildPermissionString(input.ToolName, input.ToolInput)
	sysMsg := fmt.Sprintf("[spinclass] auto-approved: %s (%s tier)", permStr, tierName)

	output := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest",
			"decision":      map[string]any{"behavior": "allow"},
		},
		"systemMessage": sysMsg,
	}

	return json.NewEncoder(w).Encode(output)
}

// repoFromCWD extracts the repository name from a working directory path by
// matching the convention-based patterns: .../worktrees/<repo>/... or
// .../repos/<repo>/...
func repoFromCWD(cwd string) string {
	parts := strings.Split(filepath.ToSlash(cwd), "/")

	for i, part := range parts {
		if (part == "worktrees" || part == "repos") && i+1 < len(parts) {
			return parts[i+1]
		}
	}

	return ""
}
