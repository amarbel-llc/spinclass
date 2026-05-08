package main

import (
	"strings"
	"testing"
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
