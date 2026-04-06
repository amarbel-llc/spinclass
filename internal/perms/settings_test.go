package perms

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadClaudeSettings(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".claude", "settings.local.json")

	os.MkdirAll(filepath.Dir(path), 0o755)

	settings := map[string]any{
		"permissions": map[string]any{
			"allow": []string{"Read", "Edit", "Bash(go test:*)"},
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	os.WriteFile(path, data, 0o644)

	rules, err := LoadClaudeSettings(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}
	if rules[0] != "Read" {
		t.Errorf("expected Read, got %q", rules[0])
	}
	if rules[1] != "Edit" {
		t.Errorf("expected Edit, got %q", rules[1])
	}
	if rules[2] != "Bash(go test:*)" {
		t.Errorf("expected Bash(go test:*), got %q", rules[2])
	}
}

func TestLoadClaudeSettingsMissing(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".claude", "settings.local.json")

	rules, err := LoadClaudeSettings(path)
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if rules != nil {
		t.Errorf("expected nil for missing file, got %v", rules)
	}
}

func TestSaveClaudeSettings(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "sub", ".claude", "settings.local.json")

	rules := []string{"Read", "Bash(git *)", "mcp__plugin_nix_nix__build"}
	err := SaveClaudeSettings(path, rules)
	if err != nil {
		t.Fatalf("unexpected error saving: %v", err)
	}

	loaded, err := LoadClaudeSettings(path)
	if err != nil {
		t.Fatalf("unexpected error loading: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(loaded))
	}
	if loaded[0] != "Read" {
		t.Errorf("expected Read, got %q", loaded[0])
	}
	if loaded[1] != "Bash(git *)" {
		t.Errorf("expected Bash(git *), got %q", loaded[1])
	}
	if loaded[2] != "mcp__plugin_nix_nix__build" {
		t.Errorf("expected mcp__plugin_nix_nix__build, got %q", loaded[2])
	}
}

func TestDiffRules(t *testing.T) {
	before := []string{"Read", "Edit"}
	after := []string{"Read", "Edit", "Bash(go test:*)", "Write"}

	diff := DiffRules(before, after)
	if len(diff) != 2 {
		t.Fatalf("expected 2 new rules, got %d: %v", len(diff), diff)
	}
	if diff[0] != "Bash(go test:*)" {
		t.Errorf("expected Bash(go test:*), got %q", diff[0])
	}
	if diff[1] != "Write" {
		t.Errorf("expected Write, got %q", diff[1])
	}
}

func TestDiffRulesNoChanges(t *testing.T) {
	rules := []string{"Read", "Edit", "Bash(go test:*)"}

	diff := DiffRules(rules, rules)
	if len(diff) != 0 {
		t.Errorf("expected no diff for identical rules, got %v", diff)
	}
}

func TestSaveClaudeSettingsPreservesOtherFields(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.local.json")

	// Write a file with extra fields beyond permissions.allow
	initial := `{
  "permissions": {
    "allow": ["Read", "Edit"],
    "deny": ["Bash(rm -rf:*)"]
  },
  "env": {
    "FOO": "bar"
  }
}
`
	os.WriteFile(path, []byte(initial), 0o644)

	// Save with updated allow list
	err := SaveClaudeSettings(path, []string{"Read"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read back raw JSON and verify other fields survived
	data, _ := os.ReadFile(path)
	var doc map[string]any
	json.Unmarshal(data, &doc)

	permsMap, ok := doc["permissions"].(map[string]any)
	if !ok {
		t.Fatal("expected permissions key")
	}
	deny, ok := permsMap["deny"].([]any)
	if !ok || len(deny) != 1 {
		t.Errorf("expected deny list preserved, got %v", permsMap["deny"])
	}
	envMap, ok := doc["env"].(map[string]any)
	if !ok {
		t.Fatal("expected env key preserved")
	}
	if envMap["FOO"] != "bar" {
		t.Errorf("expected env.FOO=bar, got %v", envMap["FOO"])
	}
}

func TestRemoveRules(t *testing.T) {
	rules := []string{"Read", "Edit", "Bash(go test:*)", "Write"}
	toRemove := []string{"Edit", "Write"}

	result := RemoveRules(rules, toRemove)
	if len(result) != 2 {
		t.Fatalf("expected 2 remaining rules, got %d: %v", len(result), result)
	}
	if result[0] != "Read" {
		t.Errorf("expected Read, got %q", result[0])
	}
	if result[1] != "Bash(go test:*)" {
		t.Errorf("expected Bash(go test:*), got %q", result[1])
	}
}

func TestLoadRulesFromLog(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "tool-use.log")

	// Write JSONL entries
	lines := []string{
		`{"tool_name":"Edit","tool_input":{"file_path":"/some/file.go"}}`,
		`{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`,
		`{"tool_name":"Edit","tool_input":{"file_path":"/some/file.go"}}`,
		`{"tool_name":"Glob","tool_input":{}}`,
	}
	os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	rules, err := LoadRulesFromLog(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should deduplicate: Edit(/some/file.go) appears twice
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d: %v", len(rules), rules)
	}
	if rules[0] != "Edit(/some/file.go)" {
		t.Errorf("expected Edit(/some/file.go), got %q", rules[0])
	}
	if rules[1] != "Bash(go test ./...)" {
		t.Errorf("expected Bash(go test ./...), got %q", rules[1])
	}
	if rules[2] != "Glob" {
		t.Errorf("expected Glob, got %q", rules[2])
	}
}

func TestLoadRulesFromLogMissing(t *testing.T) {
	rules, err := LoadRulesFromLog(filepath.Join(t.TempDir(), "nonexistent.log"))
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if rules != nil {
		t.Errorf("expected nil for missing file, got %v", rules)
	}
}

func TestDiscoverToolUseLogs(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_LOG_HOME", tmpDir)

	// Empty / missing directory
	logs, err := DiscoverToolUseLogs()
	if err != nil {
		t.Fatalf("expected no error for missing dir, got %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected no logs for missing dir, got %v", logs)
	}

	// Populate dir with mixed file types
	logDir := filepath.Join(tmpDir, "spinclass", "tool-uses")
	os.MkdirAll(logDir, 0o755)
	os.WriteFile(filepath.Join(logDir, "repoA--branch1.jsonl"), []byte("{}\n"), 0o644)
	os.WriteFile(filepath.Join(logDir, "repoB--branch2.jsonl"), []byte("{}\n"), 0o644)
	os.WriteFile(filepath.Join(logDir, "ignored.txt"), []byte("noise\n"), 0o644)
	os.MkdirAll(filepath.Join(logDir, "subdir"), 0o755)

	logs, err = DiscoverToolUseLogs()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs, got %d: %v", len(logs), logs)
	}
	// Sorted order
	if !strings.HasSuffix(logs[0], "repoA--branch1.jsonl") {
		t.Errorf("expected repoA log first, got %q", logs[0])
	}
	if !strings.HasSuffix(logs[1], "repoB--branch2.jsonl") {
		t.Errorf("expected repoB log second, got %q", logs[1])
	}
}

func TestComputeReviewableRulesAll(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", "/Users/me")
	t.Setenv("XDG_LOG_HOME", tmpDir)

	logDir := filepath.Join(tmpDir, "spinclass", "tool-uses")
	os.MkdirAll(logDir, 0o755)

	// Two sessions with overlapping rules
	os.WriteFile(filepath.Join(logDir, "repoA--main.jsonl"), []byte(strings.Join([]string{
		`{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`,
		`{"tool_name":"Bash","tool_input":{"command":"nix build"}}`,
		`{"tool_name":"Edit","tool_input":{"file_path":"/Users/me/repos/A/main.go"}}`,
		`{"tool_name":"Glob","tool_input":{}}`,
		`{"tool_name":"Read","tool_input":{"file_path":"/Users/me/.claude/CLAUDE.md"}}`,
		`{"tool_name":"mcp__plugin_grit_grit__add","tool_input":{}}`,
	}, "\n")+"\n"), 0o644)
	os.WriteFile(filepath.Join(logDir, "repoB--feat.jsonl"), []byte(strings.Join([]string{
		`{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`, // dup
		`{"tool_name":"Bash","tool_input":{"command":"cargo test"}}`,
		`{"tool_name":"WebSearch","tool_input":{}}`,
	}, "\n")+"\n"), 0o644)

	// Global Claude settings cover Glob and WebSearch
	globalSettingsPath := filepath.Join(tmpDir, "global-settings.json")
	if err := SaveClaudeSettings(globalSettingsPath, []string{
		"Glob",
		"WebSearch",
	}); err != nil {
		t.Fatal(err)
	}

	// Tier files: global tier excludes Edit; per-repo tier includes
	// Bash(go test ./...) — should NOT be excluded in --all mode.
	tiersDir := filepath.Join(tmpDir, "tiers")
	os.MkdirAll(filepath.Join(tiersDir, "repos"), 0o755)
	if err := SaveTierFile(filepath.Join(tiersDir, "global.json"), Tier{Allow: []string{"Edit"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveTierFile(filepath.Join(tiersDir, "repos", "repoA.json"), Tier{Allow: []string{"Bash(go test ./...)"}}); err != nil {
		t.Fatal(err)
	}

	got, err := ComputeReviewableRulesAll(tiersDir, globalSettingsPath, true)
	if err != nil {
		t.Fatal(err)
	}

	// Should contain (deduped across logs):
	// - Bash(go test ./...) — repo tier rule does NOT exclude it in --all
	// - Bash(nix build)
	// - Bash(cargo test)
	// - mcp__plugin_grit_grit__add
	// Should NOT contain:
	// - Edit(/Users/me/repos/A/main.go)  — bare Edit in global tier matches
	// - Glob, WebSearch                  — in global Claude settings
	// - Read(/Users/me/.claude/CLAUDE.md) — auto-injected ~/.claude exclusion
	wantSet := map[string]bool{
		"Bash(go test ./...)":        true,
		"Bash(nix build)":            true,
		"Bash(cargo test)":           true,
		"mcp__plugin_grit_grit__add": true,
	}

	gotSet := map[string]bool{}
	for _, r := range got {
		gotSet[r] = true
	}

	for want := range wantSet {
		if !gotSet[want] {
			t.Errorf("expected %q in reviewable rules, got %v", want, got)
		}
	}
	for _, r := range got {
		if !wantSet[r] {
			t.Errorf("unexpected rule %q in reviewable rules", r)
		}
	}
}

func TestComputeReviewableRulesAllEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", "/Users/me")
	t.Setenv("XDG_LOG_HOME", tmpDir)

	got, err := ComputeReviewableRulesAll(
		filepath.Join(tmpDir, "tiers"),
		filepath.Join(tmpDir, "global-settings.json"),
		true,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no rules for empty log dir, got %v", got)
	}
}

func TestComputeReviewableRules(t *testing.T) {
	tmpDir := t.TempDir()

	// Override HOME so the auto-injected Read(<home>/.claude/*) exclusion
	// matches the synthetic /Users/me paths used in this test.
	t.Setenv("HOME", "/Users/me")

	// Tool-use log with a mix of tool invocations
	logPath := filepath.Join(tmpDir, "tool-use.log")
	logEntries := []string{
		`{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`,
		`{"tool_name":"Bash","tool_input":{"command":"nix build"}}`,
		`{"tool_name":"Edit","tool_input":{"file_path":"/Users/me/repos/bob/.worktrees/wt/main.go"}}`,
		`{"tool_name":"Glob","tool_input":{}}`,
		`{"tool_name":"Read","tool_input":{"file_path":"/Users/me/.claude/CLAUDE.md"}}`,
		`{"tool_name":"Read","tool_input":{"file_path":"/Users/me/repos/bob/.worktrees/wt/go.mod"}}`,
		`{"tool_name":"Edit","tool_input":{"file_path":"/Users/me/repos/bob/.worktrees/wt/pkg/foo.go"}}`,
		`{"tool_name":"Write","tool_input":{"file_path":"/Users/me/repos/bob/.worktrees/wt/new.go"}}`,
		`{"tool_name":"mcp__plugin_grit_grit__add","tool_input":{}}`,
		`{"tool_name":"WebSearch","tool_input":{}}`,
	}
	os.WriteFile(logPath, []byte(strings.Join(logEntries, "\n")+"\n"), 0o644)

	// Global settings: some overlap
	globalSettingsPath := filepath.Join(tmpDir, "global-settings.json")
	if err := SaveClaudeSettings(globalSettingsPath, []string{
		"Glob",
		"WebSearch",
	}); err != nil {
		t.Fatal(err)
	}

	// Tier files: some overlap
	tiersDir := filepath.Join(tmpDir, "tiers")
	os.MkdirAll(filepath.Join(tiersDir, "repos"), 0o755)
	if err := SaveTierFile(filepath.Join(tiersDir, "global.json"), Tier{Allow: []string{"Edit"}}); err != nil {
		t.Fatal(err)
	}

	got, err := ComputeReviewableRules(
		logPath,
		globalSettingsPath,
		tiersDir,
		"myrepo",
		"/Users/me/repos/bob/.worktrees/wt",
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Should contain: Bash(go test ./...), Bash(nix build), mcp__plugin_grit_grit__add
	// Should NOT contain: Edit (bare, in tier), Glob (global), WebSearch (global),
	// Read/Edit/Write worktree paths, Read(~/.claude/*)
	wantSet := map[string]bool{
		"Bash(go test ./...)":        true,
		"Bash(nix build)":            true,
		"mcp__plugin_grit_grit__add": true,
	}

	gotSet := map[string]bool{}
	for _, r := range got {
		gotSet[r] = true
	}

	for want := range wantSet {
		if !gotSet[want] {
			t.Errorf("expected %q in reviewable rules", want)
		}
	}

	for _, r := range got {
		if !wantSet[r] {
			t.Errorf("unexpected rule %q in reviewable rules", r)
		}
	}
}

func TestComputeReviewableRules_FilterNonMCP(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", "/Users/me")

	// Write a log with mixed MCP and non-MCP rules.
	logDir := filepath.Join(tmpDir, "spinclass", "tool-uses")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "myrepo--main.jsonl")
	logEntries := strings.Join([]string{
		`{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`,
		`{"tool_name":"Bash","tool_input":{"command":"git status"}}`,
		`{"tool_name":"Read","tool_input":{"file_path":"/tmp/foo.go"}}`,
		`{"tool_name":"mcp__plugin_grit_grit__add","tool_input":{}}`,
		`{"tool_name":"mcp__plugin_chix_chix__build","tool_input":{}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(logEntries), 0o644); err != nil {
		t.Fatal(err)
	}

	// Empty global settings and tiers — nothing excluded by those.
	globalSettingsPath := filepath.Join(tmpDir, "settings.json")
	if err := os.WriteFile(globalSettingsPath, []byte(`{"permissions":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tiersDir := filepath.Join(tmpDir, "tiers")
	if err := os.MkdirAll(tiersDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// With includeBuiltin=false, only MCP rules should be returned.
	got, err := ComputeReviewableRules(
		logPath, globalSettingsPath, tiersDir, "myrepo", "/Users/me/repos/myrepo/.worktrees/wt",
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range got {
		if FriendlyName(r) == "" {
			t.Errorf("non-MCP rule %q should have been filtered out", r)
		}
	}
	if len(got) != 2 {
		t.Errorf("expected 2 MCP rules, got %d: %v", len(got), got)
	}

	// With includeBuiltin=true, all non-excluded rules should be returned.
	gotAll, err := ComputeReviewableRules(
		logPath, globalSettingsPath, tiersDir, "myrepo", "/Users/me/repos/myrepo/.worktrees/wt",
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(gotAll) < len(got) {
		t.Errorf("includeBuiltin=true returned fewer rules (%d) than false (%d)", len(gotAll), len(got))
	}
}
