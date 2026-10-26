package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hookVerdictsName is the worktree-local log of PreToolUse decisions on
// spinclass MCP tools. A call that "ran" is ambiguous: an allow and an ask the
// operator approved both just complete. This line is the hook's own verdict, so
// a test session can assert on it instead of on whether a prompt appeared.
//
// It is an observability log, NOT an authority. The worktree's .spinclass dir is
// writable by the agent (confined or not), so a hostile agent can forge or
// delete lines; it exists so honest test sessions can see what the hook decided.
const hookVerdictsName = "hook-verdicts.ndjson"

// hookVerdictsMaxBytes caps the log; past it, appends stop.
const hookVerdictsMaxBytes = 256 << 10

// hookVerdictInputMaxBytes caps the logged tool_input JSON per line.
const hookVerdictInputMaxBytes = 512

// verdictToolPrefix scopes the log to spinclass's own MCP tools. Other tools
// (Bash, Read, …) can carry secrets in their inputs and are never logged.
const verdictToolPrefix = "mcp__plugin_spinclass_spinclass__"

type verdictLine struct {
	TS        string `json:"ts"`
	Tool      string `json:"tool"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason"`
	Input     string `json:"input"`
	ToolUseID string `json:"tool_use_id,omitempty"`
}

// logVerdict appends one NDJSON line describing the PreToolUse output out (the
// bytes about to be written to Claude Code; empty means the hook decided
// nothing). Best-effort and silent on any failure, like hookTrace: it never
// creates .spinclass and never blocks or fails the hook.
func logVerdict(input hookInput, out []byte) {
	if !strings.HasPrefix(input.ToolName, verdictToolPrefix) || input.CWD == "" {
		return
	}
	dir := filepath.Join(input.CWD, ".spinclass")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return
	}
	path := filepath.Join(dir, hookVerdictsName)
	if fi, err := os.Stat(path); err == nil && fi.Size() > hookVerdictsMaxBytes {
		return
	}

	decision, reason := decodeVerdict(out)
	line, err := json.Marshal(verdictLine{
		TS:        time.Now().UTC().Format(time.RFC3339Nano),
		Tool:      input.ToolName,
		Decision:  decision,
		Reason:    reason,
		Input:     truncatedInput(input.ToolInput),
		ToolUseID: input.ToolUseID,
	})
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// decodeVerdict reads the permission decision out of the hook's PreToolUse
// output: "none" (with no reason) when the hook emitted nothing or nothing it
// recognises, leaving Claude Code's own permission rules to decide.
func decodeVerdict(out []byte) (decision, reason string) {
	if len(bytes.TrimSpace(out)) == 0 {
		return "none", ""
	}
	var v struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &v); err != nil || v.HookSpecificOutput.PermissionDecision == "" {
		return "none", ""
	}
	return v.HookSpecificOutput.PermissionDecision, v.HookSpecificOutput.PermissionDecisionReason
}

// truncatedInput is the compact tool_input JSON, cut to
// hookVerdictInputMaxBytes with a trailing "…" when longer.
func truncatedInput(toolInput map[string]any) string {
	b, err := json.Marshal(toolInput)
	if err != nil {
		return ""
	}
	if len(b) <= hookVerdictInputMaxBytes {
		return string(b)
	}
	return string(b[:hookVerdictInputMaxBytes]) + "…"
}
