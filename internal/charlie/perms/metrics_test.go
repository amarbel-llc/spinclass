package perms

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testfs"
)

// captureMetrics installs a stub CountMetric and returns the names it saw.
func captureMetrics(t *testing.T) *[]string {
	t.Helper()
	var names []string
	prev := CountMetric
	CountMetric = func(_, name string) { names = append(names, name) }
	t.Cleanup(func() { CountMetric = prev })
	return &names
}

func runCheckWith(t *testing.T, tiersDir string, payload map[string]any) string {
	t.Helper()
	data, _ := json.Marshal(payload)
	var out bytes.Buffer
	if err := RunCheck(bytes.NewReader(data), &out, tiersDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return out.String()
}

func globalTierDir(t *testing.T, allow ...string) string {
	t.Helper()
	dir := t.TempDir()
	data, _ := json.Marshal(Tier{Allow: allow})
	testfs.MustWriteFile(t, filepath.Join(dir, "global.json"), data, 0o644)
	return dir
}

const metricsCWD = "/home/user/eng/repos/My.Repo/.worktrees/feature"

func expectNames(t *testing.T, got *[]string, want ...string) {
	t.Helper()
	if len(*got) != len(want) {
		t.Fatalf("metrics = %v, want %v", *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Errorf("metric[%d] = %q, want %q", i, (*got)[i], want[i])
		}
	}
}

func TestCheckCountsPromptWhenNoRuleMatches(t *testing.T) {
	names := captureMetrics(t)
	dir := globalTierDir(t, "Bash(go test:*)")
	runCheckWith(t, dir, map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf /"}, "cwd": metricsCWD,
	})
	expectNames(t, names, "permission.my_repo.prompt.main.bash")
}

func TestCheckCountsSubagentPrompt(t *testing.T) {
	names := captureMetrics(t)
	dir := globalTierDir(t, "Bash(go test:*)")
	runCheckWith(t, dir, map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf /"}, "cwd": metricsCWD,
		"agent_id": "agent-abc123",
	})
	expectNames(t, names, "permission.my_repo.prompt.subagent.bash")
}

func TestCheckCountsTierAllow(t *testing.T) {
	names := captureMetrics(t)
	dir := globalTierDir(t, "Bash(go test:*)")
	out := runCheckWith(t, dir, map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}, "cwd": metricsCWD,
		"agent_id": "agent-abc123",
	})
	if out == "" {
		t.Fatal("precondition: expected an allow")
	}
	expectNames(t, names, "permission.my_repo.tier_allow.subagent.bash")
}

func TestCheckCountsAlwaysAskDeferralAsPrompt(t *testing.T) {
	names := captureMetrics(t)
	dir := globalTierDir(t, "mcp__plugin_spinclass_spinclass__spawn-session")
	runCheckWith(t, dir, map[string]any{
		"tool_name": "mcp__plugin_spinclass_spinclass__spawn-session", "tool_input": map[string]any{}, "cwd": metricsCWD,
	})
	expectNames(t, names, "permission.my_repo.prompt.main.mcp__plugin_spinclass_spinclass__spawn-session")
}

func TestCheckMetricsUnknownRepoFallback(t *testing.T) {
	names := captureMetrics(t)
	dir := globalTierDir(t)
	runCheckWith(t, dir, map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "x"}, "cwd": "/tmp/random",
	})
	expectNames(t, names, "permission.unknown.prompt.main.bash")
}

func TestCountPermissionEventSanitizesSegments(t *testing.T) {
	names := captureMetrics(t)
	CountPermissionEvent(metricsCWD, "prompt", "", "mcp__a.b c")
	expectNames(t, names, "permission.my_repo.prompt.main.mcp__a_b_c")
}

func TestCountPermissionEventNilSeamIsNoop(t *testing.T) {
	prev := CountMetric
	CountMetric = nil
	t.Cleanup(func() { CountMetric = prev })
	CountPermissionEvent(metricsCWD, "prompt", "", "Bash") // must not panic
}
