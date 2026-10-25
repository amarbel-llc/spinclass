package perms

import (
	"net/url"
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

	// Rules should be percent-encoded in the output
	bashEncoded := url.PathEscape("Bash(go test:*)")
	if !strings.Contains(got, "discard "+bashEncoded) {
		t.Errorf("expected percent-encoded Bash rule in output, got:\n%s", got)
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
		if strings.Contains(line, bashEncoded) {
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
	// Input uses percent-encoded rules (as produced by FormatEditorContent).
	input := "# comment line\n# another comment\n\n" +
		"global " + url.PathEscape("mcp__plugin_grit_grit__add") + "                    # grit:add\n" +
		"repo   " + url.PathEscape("Bash(go test:*)") + "\n" +
		"keep   " + url.PathEscape("Bash(nix build:*)") + "\n" +
		"discard " + url.PathEscape("mcp__plugin_chix_chix__build") + "                 # chix:build\n"

	decisions, err := ParseEditorContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(decisions) != 4 {
		t.Fatalf("expected 4 decisions, got %d", len(decisions))
	}

	// Decoded rules should match the originals.
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
	input := "g " + url.PathEscape("Bash(git status)") + "\n" +
		"r " + url.PathEscape("Bash(go test:*)") + "\n" +
		"k Edit\n" +
		"d " + url.PathEscape("Bash(rm -rf:*)") + "\n"
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
	input := "xyz " + url.PathEscape("Bash(git status)") + "\n"
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

// Tests for issue #8: multi-line rules break the editor round-trip.

func TestFormatParseRoundTrip_MultiLineRule(t *testing.T) {
	// A Bash command captured from a tool-use log that contains an embedded
	// newline (heredoc, multi-line script, JSON payload, etc.).
	multiLineRule := "Bash(cat <<EOF\nfoo\nbar\nEOF)"

	rules := []string{
		"mcp__plugin_grit_grit__add",
		multiLineRule,
	}

	formatted := FormatEditorContent(rules, "myrepo")
	// The formatted content should be parseable back without error.
	// Currently fails because the newlines in the rule split it across
	// multiple physical lines, and the parser treats each line as
	// "action rule".
	decisions, err := ParseEditorContent(formatted)
	if err != nil {
		t.Fatalf("round-trip failed: format then parse returned error: %v", err)
	}

	// We should get exactly 2 decisions back (one per rule).
	if len(decisions) != 2 {
		t.Fatalf("expected 2 decisions from round-trip, got %d", len(decisions))
	}

	// The multi-line rule should survive the round-trip intact.
	found := false
	for _, d := range decisions {
		if d.Rule == multiLineRule {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("multi-line rule did not survive round-trip; got rules: %v",
			func() []string {
				var rs []string
				for _, d := range decisions {
					rs = append(rs, d.Rule)
				}
				return rs
			}())
	}
}

func TestParseEditorContent_MultiLineRulePercentEncoded(t *testing.T) {
	// With percent-encoding, a multi-line rule is a single physical line.
	multiLineRule := "Bash(cat <<HEREDOC\n// this is a comment inside the script\necho done\nHEREDOC)"
	input := "discard " + url.PathEscape(multiLineRule) + "\n" +
		"discard " + url.PathEscape("mcp__plugin_grit_grit__add") + "  # grit:add\n"

	decisions, err := ParseEditorContent(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(decisions))
	}
	if decisions[0].Rule != multiLineRule {
		t.Errorf("decision[0].Rule = %q, want %q", decisions[0].Rule, multiLineRule)
	}
}

func TestWriteRuleLines_EmbeddedNewlineProducesMultiplePhysicalLines(t *testing.T) {
	// Demonstrates that writeRuleLines does not escape or quote rules that
	// contain newlines, producing a buffer where one logical rule spans
	// multiple physical lines.
	rules := []string{"Bash(line1\nline2)"}

	var b strings.Builder
	writeRuleLines(&b, rules)
	output := b.String()

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	// If the rule is properly handled, we'd expect 1 logical entry.
	// Currently we get 2 physical lines — proving the bug.
	if len(lines) != 1 {
		t.Errorf("rule with embedded newline produced %d physical lines (want 1): %v",
			len(lines), lines)
	}
}

// Tests for issue #8: non-MCP rules should be filterable.

func TestFormatEditorContent_NonMCPRulesDominateBuffer(t *testing.T) {
	// Simulates a realistic review buffer where the interesting MCP rules
	// are buried among many built-in tool rules.
	rules := []string{
		"Bash(go test ./...)",
		"Bash(rm -rf .tmp/foo123)",
		"Bash(git status)",
		"Bash(nix build)",
		"Bash(cat /etc/hosts)",
		"Read(/home/user/project/main.go)",
		"Write(/home/user/project/out.txt)",
		"Edit(/home/user/project/lib.go)",
		"Glob(*.go)",
		"Grep(TODO)",
		"mcp__plugin_grit_grit__add",
		"mcp__plugin_chix_chix__build",
	}

	formatted := FormatEditorContent(rules, "myrepo")
	lines := strings.Split(formatted, "\n")

	var mcpLines, nonMCPLines int
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// MCP rules don't contain special chars, so they appear unencoded.
		if strings.Contains(line, "mcp__") {
			mcpLines++
		} else {
			nonMCPLines++
		}
	}

	// The 2 MCP rules are the ones worth curating. The 10 non-MCP rules
	// are noise. This test documents that non-MCP rules dominate the buffer
	// and there is currently no way to filter them out.
	if nonMCPLines <= mcpLines {
		t.Errorf("expected non-MCP lines (%d) to outnumber MCP lines (%d)",
			nonMCPLines, mcpLines)
	}

	// There is no FormatEditorContentMCPOnly or similar — this test will
	// need updating once filtering is implemented.
	t.Logf("buffer has %d non-MCP lines vs %d MCP lines — non-MCP rules dominate",
		nonMCPLines, mcpLines)
}
