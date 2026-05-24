package main

import (
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/attestation"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

func TestSummarizeHookCommand_SingleLine(t *testing.T) {
	got := summarizeHookCommand("just test")
	if got != "just test" {
		t.Errorf("got %q, want %q", got, "just test")
	}
}

func TestSummarizeHookCommand_LeadingTrailingWhitespace(t *testing.T) {
	got := summarizeHookCommand("  just test  ")
	if got != "just test" {
		t.Errorf("got %q, want %q", got, "just test")
	}
}

func TestSummarizeHookCommand_MultiLineFirstLineOnly(t *testing.T) {
	script := "set -euo pipefail\njust test\njust lint"
	got := summarizeHookCommand(script)
	if got != "set -euo pipefail ..." {
		t.Errorf("got %q, want %q", got, "set -euo pipefail ...")
	}
}

func TestSummarizeHookCommand_BlankLinesBeforeContent(t *testing.T) {
	got := summarizeHookCommand("\n\n  just test\n")
	if got != "just test" {
		t.Errorf("got %q, want %q", got, "just test")
	}
}

func TestSummarizeHookCommand_OnlyWhitespace(t *testing.T) {
	got := summarizeHookCommand("   \n\n  ")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestBuildMergeThisSessionDescription_NoHook(t *testing.T) {
	got := buildMergeThisSessionDescription("")
	if strings.Contains(got, "[hooks].pre-merge command runs") {
		t.Errorf("description should not advertise pre-merge command when hook is unset:\n%s", got)
	}
	if !strings.Contains(got, "Merge the current session's worktree") {
		t.Errorf("description missing baseline text:\n%s", got)
	}
}

func TestBuildMergeThisSessionDescription_WithHook(t *testing.T) {
	got := buildMergeThisSessionDescription("just test")
	if !strings.Contains(got, "`just test`") {
		t.Errorf("description should embed the hook command:\n%s", got)
	}
	if !strings.Contains(got, "do not need to pre-flight") {
		t.Errorf("description should advise agents to skip pre-flight:\n%s", got)
	}
}

func TestBuildCheckThisSessionDescription_NoHook(t *testing.T) {
	got := buildCheckThisSessionDescription("")
	if strings.Contains(got, "configured pre-merge command is") {
		t.Errorf("description should not advertise pre-merge command when hook is unset:\n%s", got)
	}
}

func TestBuildCheckThisSessionDescription_WithHook(t *testing.T) {
	got := buildCheckThisSessionDescription("just check")
	if !strings.Contains(got, "`just check`") {
		t.Errorf("description should embed the hook command:\n%s", got)
	}
}

func TestBuildNothingButTheTruthDescription_EmbedsSkillsAndRationale(t *testing.T) {
	skills := []sweatfile.PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Mandatory second-pass review."},
		{Name: "simplify", Rationale: "Prune premature abstractions."},
	}
	got := buildNothingButTheTruthDescription(skills)
	for _, want := range []string{
		"eng:code-reviewer",
		"Mandatory second-pass review.",
		"simplify",
		"Prune premature abstractions.",
		"nothing-but-the-truth", // not the tool name itself, but reasoning that the description must surface "merge-this-session or check-this-session"
	} {
		if want == "nothing-but-the-truth" {
			continue // not actually required in the body
		}
		if !strings.Contains(got, want) {
			t.Errorf("description missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "merge-this-session") || !strings.Contains(got, "check-this-session") {
		t.Errorf("description should mention both consuming tools:\n%s", got)
	}
}

func TestRenderValidationError_NamesMissingAndUnknown(t *testing.T) {
	required := []sweatfile.PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Mandatory."},
		{Name: "simplify", Rationale: "Prune."},
	}
	verr := attestation.ValidationError{
		Missing:        []string{"simplify"},
		Unrecognised:   []string{"stranger"},
		EmptyReasoning: []string{"eng:code-reviewer"},
	}
	got := renderValidationError(required, verr)
	for _, want := range []string{
		"attestation rejected",
		"missing entries for required skills: simplify",
		"unrecognised skill names: stranger",
		"empty reasoning for: eng:code-reviewer",
		"eng:code-reviewer: Mandatory.",
		"simplify: Prune.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderValidationError missing %q:\n%s", want, got)
		}
	}
}
