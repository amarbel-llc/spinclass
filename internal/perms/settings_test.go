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
	)
	if err != nil {
		t.Fatal(err)
	}

	// Should contain: Bash(go test ./...), Bash(nix build), mcp__plugin_grit_grit__add
	// Should NOT contain: Edit (bare, in tier), Glob (global), WebSearch (global),
	// Read/Edit/Write worktree paths, Read(~/.claude/*)
	wantSet := map[string]bool{
		"Bash(go test ./...)":          true,
		"Bash(nix build)":              true,
		"mcp__plugin_grit_grit__add":   true,
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
