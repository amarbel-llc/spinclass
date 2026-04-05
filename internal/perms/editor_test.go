package perms

import (
	"strings"
	"testing"
)

func TestFriendlyName(t *testing.T) {
	tests := []struct {
		rule string
		want string
	}{
		{"mcp__plugin_grit_grit__add", "grit:add"},
		{"mcp__plugin_chix_chix__build", "chix:build"},
		{"mcp__plugin_get-hubbed_get-hubbed__issue-create", "get-hubbed:issue-create"},
		{"mcp__plugin_lux_lux__hover", "lux:hover"},
		{"Bash(go test:*)", ""},
		{"Read", ""},
		{"Glob", ""},
		{"mcp__glean_default__search", "glean:search"},
	}

	for _, tt := range tests {
		t.Run(tt.rule, func(t *testing.T) {
			got := FriendlyName(tt.rule)
			if got != tt.want {
				t.Errorf("FriendlyName(%q) = %q, want %q", tt.rule, got, tt.want)
			}
		})
	}
}

func TestFormatEditorContent(t *testing.T) {
	rules := []string{
		"mcp__plugin_grit_grit__add",
		"Bash(go test:*)",
		"mcp__plugin_chix_chix__build",
	}
	got := FormatEditorContent(rules, "myrepo")

	// Should be sorted alphabetically
	if !strings.Contains(got, "discard Bash(go test:*)") {
		t.Error("expected Bash rule in output")
	}
	if !strings.Contains(got, "# chix:build") {
		t.Error("expected chix:build friendly name comment")
	}
	if !strings.Contains(got, "# grit:add") {
		t.Error("expected grit:add friendly name comment")
	}
	// Bash rule should NOT have a friendly name comment
	lines := strings.Split(got, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "discard Bash(go test:*)") {
			if strings.Contains(line, "#") {
				t.Error("non-MCP rule should not have a friendly name comment")
			}
		}
	}
	// Header should contain repo name
	if !strings.Contains(got, "# Repo: myrepo") {
		t.Error("expected repo name in header")
	}
}

func TestParseEditorContent(t *testing.T) {
	input := `# comment line
# another comment

global mcp__plugin_grit_grit__add                    # grit:add
repo   Bash(go test:*)
keep   Bash(nix build:*)
discard mcp__plugin_chix_chix__build                 # chix:build
`
	decisions, err := ParseEditorContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(decisions) != 4 {
		t.Fatalf("expected 4 decisions, got %d", len(decisions))
	}

	expected := []ReviewDecision{
		{Rule: "mcp__plugin_grit_grit__add", Action: ReviewPromoteGlobal},
		{Rule: "Bash(go test:*)", Action: ReviewPromoteRepo},
		{Rule: "Bash(nix build:*)", Action: ReviewKeep},
		{Rule: "mcp__plugin_chix_chix__build", Action: ReviewDiscard},
	}

	for i, want := range expected {
		if decisions[i].Rule != want.Rule {
			t.Errorf("decision[%d].Rule = %q, want %q", i, decisions[i].Rule, want.Rule)
		}
		if decisions[i].Action != want.Action {
			t.Errorf("decision[%d].Action = %q, want %q", i, decisions[i].Action, want.Action)
		}
	}
}

func TestParseEditorContentPrefixes(t *testing.T) {
	input := `g Bash(git status)
r Bash(go test:*)
k Edit
d Bash(rm -rf:*)
`
	decisions, err := ParseEditorContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(decisions) != 4 {
		t.Fatalf("expected 4 decisions, got %d", len(decisions))
	}

	if decisions[0].Action != ReviewPromoteGlobal {
		t.Errorf("expected global, got %q", decisions[0].Action)
	}
	if decisions[1].Action != ReviewPromoteRepo {
		t.Errorf("expected repo, got %q", decisions[1].Action)
	}
	if decisions[2].Action != ReviewKeep {
		t.Errorf("expected keep, got %q", decisions[2].Action)
	}
	if decisions[3].Action != ReviewDiscard {
		t.Errorf("expected discard, got %q", decisions[3].Action)
	}
}

func TestParseEditorContentBadAction(t *testing.T) {
	input := `xyz Bash(git status)
`
	_, err := ParseEditorContent(input)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestParseEditorContentEmptyLine(t *testing.T) {
	input := `

`
	decisions, err := ParseEditorContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 0 {
		t.Fatalf("expected 0 decisions, got %d", len(decisions))
	}
}
