package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gritToolPrefix = "mcp__plugin_spinclass_spinclass__grit_"

// runVerdictHook runs one PreToolUse payload through Run with cwd as the
// session worktree and returns the hook's stdout.
func runVerdictHook(t *testing.T, cwd string, payload map[string]any) []byte {
	t.Helper()
	setEnvOrUnset(t, "CLOWN_SESSION_ID", "")
	setEnvOrUnset(t, "CLOWN_UNIT", "")
	payload["hook_event_name"] = "PreToolUse"
	payload["cwd"] = cwd
	data, _ := json.Marshal(payload)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(data), &stdout, "", cwd, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return stdout.Bytes()
}

func readVerdicts(t *testing.T, wt string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(wt, ".spinclass", hookVerdictsName))
	if err != nil {
		return nil
	}
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("verdict line is not JSON: %q: %v", l, err)
		}
		lines = append(lines, m)
	}
	return lines
}

func verdictWorktree(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, wt := initSpawnedWorktree(t, "")
	if err := os.MkdirAll(filepath.Join(wt, ".spinclass"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(wt, ".spinclass", hookVerdictsName))
	return wt
}

func TestVerdictLogRecordsAllow(t *testing.T) {
	wt := verdictWorktree(t)
	out := runVerdictHook(t, wt, map[string]any{
		"tool_name":   gritToolPrefix + "stage",
		"tool_input":  map[string]any{"subcommand": "status"},
		"tool_use_id": "toolu_123",
	})
	decision, reason := parseHookDecision(t, out)
	if decision != "allow" {
		t.Fatalf("precondition: want allow, got %q", decision)
	}
	lines := readVerdicts(t, wt)
	if len(lines) != 1 {
		t.Fatalf("want 1 verdict line, got %d", len(lines))
	}
	l := lines[0]
	if l["decision"] != "allow" || l["tool"] != gritToolPrefix+"stage" || l["reason"] != reason {
		t.Errorf("bad line: %v", l)
	}
	if l["tool_use_id"] != "toolu_123" {
		t.Errorf("tool_use_id: %v", l["tool_use_id"])
	}
	if l["input"] != `{"subcommand":"status"}` {
		t.Errorf("input: %v", l["input"])
	}
	if _, ok := l["ts"].(string); !ok {
		t.Errorf("ts missing: %v", l)
	}
}

func TestVerdictLogRecordsAsk(t *testing.T) {
	wt := verdictWorktree(t)
	out := runVerdictHook(t, wt, map[string]any{
		"tool_name":  gritToolPrefix + "branch",
		"tool_input": map[string]any{"subcommand": "delete", "name": "master"},
	})
	_, reason := parseHookDecision(t, out)
	lines := readVerdicts(t, wt)
	if len(lines) != 1 {
		t.Fatalf("want 1 verdict line, got %d", len(lines))
	}
	if lines[0]["decision"] != "ask" || lines[0]["reason"] != reason || reason == "" {
		t.Errorf("bad line: %v", lines[0])
	}
	if _, ok := lines[0]["tool_use_id"]; ok {
		t.Errorf("tool_use_id should be omitted when absent")
	}
}

// An ask in dontAsk mode leaves the hook as a deny; the log records what the
// hook actually emitted.
func TestVerdictLogRecordsDontAskDeny(t *testing.T) {
	wt := verdictWorktree(t)
	out := runVerdictHook(t, wt, map[string]any{
		"tool_name":       gritToolPrefix + "branch",
		"tool_input":      map[string]any{"subcommand": "delete", "name": "master"},
		"permission_mode": "dontAsk",
	})
	if d, _ := parseHookDecision(t, out); d != "deny" {
		t.Fatalf("precondition: want deny, got %q", d)
	}
	lines := readVerdicts(t, wt)
	if len(lines) != 1 || lines[0]["decision"] != "deny" {
		t.Fatalf("want one deny line, got %v", lines)
	}
}

func TestVerdictLogRecordsNoneWhenHookIsSilent(t *testing.T) {
	wt := verdictWorktree(t)
	out := runVerdictHook(t, wt, map[string]any{
		"tool_name":  "mcp__plugin_spinclass_spinclass__list",
		"tool_input": map[string]any{},
	})
	if len(bytes.TrimSpace(out)) != 0 {
		t.Skipf("hook emits %q for list; no silent spinclass tool to test", out)
	}
	lines := readVerdicts(t, wt)
	if len(lines) != 1 || lines[0]["decision"] != "none" || lines[0]["reason"] != "" {
		t.Fatalf("want one none line, got %v", lines)
	}
}

func TestVerdictLogSkipsNonSpinclassTools(t *testing.T) {
	wt := verdictWorktree(t)
	runVerdictHook(t, wt, map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "echo secret"},
	})
	if lines := readVerdicts(t, wt); len(lines) != 0 {
		t.Fatalf("Bash must not be logged, got %v", lines)
	}
}

func TestVerdictLogDoesNotCreateSpinclassDir(t *testing.T) {
	wt := verdictWorktree(t)
	if err := os.RemoveAll(filepath.Join(wt, ".spinclass")); err != nil {
		t.Fatal(err)
	}
	runVerdictHook(t, wt, map[string]any{
		"tool_name":  gritToolPrefix + "stage",
		"tool_input": map[string]any{"subcommand": "status"},
	})
	if _, err := os.Stat(filepath.Join(wt, ".spinclass")); err == nil {
		t.Fatal("verdict log created .spinclass")
	}
}

func TestVerdictLogLeavesHookOutputUnchanged(t *testing.T) {
	wt := verdictWorktree(t)
	payload := func() map[string]any {
		return map[string]any{
			"tool_name":  gritToolPrefix + "branch",
			"tool_input": map[string]any{"subcommand": "delete", "name": "master"},
		}
	}
	withLog := runVerdictHook(t, wt, payload())
	// Without .spinclass nothing is logged; the output must be the same bytes.
	if err := os.RemoveAll(filepath.Join(wt, ".spinclass")); err != nil {
		t.Fatal(err)
	}
	withoutLog := runVerdictHook(t, wt, payload())
	if !bytes.Equal(withLog, withoutLog) {
		t.Errorf("output differs:\nwith:    %q\nwithout: %q", withLog, withoutLog)
	}
	if len(withLog) == 0 || withLog[len(withLog)-1] != '\n' {
		t.Errorf("output should be one JSON line, got %q", withLog)
	}
}

func TestVerdictLogTruncatesLongInput(t *testing.T) {
	wt := verdictWorktree(t)
	runVerdictHook(t, wt, map[string]any{
		"tool_name":  gritToolPrefix + "commit",
		"tool_input": map[string]any{"message": strings.Repeat("x", 2000)},
	})
	lines := readVerdicts(t, wt)
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	in, _ := lines[0]["input"].(string)
	if !strings.HasSuffix(in, "…") || len(in) > hookVerdictInputMaxBytes+len("…") {
		t.Errorf("input not truncated: len=%d", len(in))
	}
}

func TestVerdictLogStopsAtCap(t *testing.T) {
	wt := verdictWorktree(t)
	path := filepath.Join(wt, ".spinclass", hookVerdictsName)
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), hookVerdictsMaxBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	runVerdictHook(t, wt, map[string]any{
		"tool_name":  gritToolPrefix + "stage",
		"tool_input": map[string]any{"subcommand": "status"},
	})
	fi, _ := os.Stat(path)
	if fi.Size() != int64(hookVerdictsMaxBytes+1) {
		t.Errorf("log grew past the cap: %d", fi.Size())
	}
}
