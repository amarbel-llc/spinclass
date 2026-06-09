package main

import (
	"os"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/attestation"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

// TestCurrentSessionKeyImplicitFallback verifies that when not inside a
// worktree and $SPINCLASS_SESSION_ID is unset, currentSessionKey resolves the
// key from a live implicit (main-checkout) session's state file rather than
// erroring — the path chat-send/chat-read need from a main checkout (#118).
func TestCurrentSessionKeyImplicitFallback(t *testing.T) {
	t.Setenv("SPINCLASS_SESSION_ID", "") // force the fallback path
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// A bare tempdir has no .git file, so worktree.IsWorktree is false and the
	// implicit fallback fires before any git call.
	repo := t.TempDir()
	st := session.State{
		Kind:         session.KindImplicit,
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		RepoPath:     repo,
		WorktreePath: repo,
		Branch:       "master",
		SessionKey:   "myrepo/master-cafe1234",
	}
	if err := session.WriteImplicit(st, "cafe1234"); err != nil {
		t.Fatalf("WriteImplicit: %v", err)
	}
	t.Chdir(repo)

	key, err := currentSessionKey()
	if err != nil {
		t.Fatalf("expected implicit fallback to resolve, got error: %v", err)
	}
	if key != "myrepo/master-cafe1234" {
		t.Fatalf("key = %q, want myrepo/master-cafe1234", key)
	}
}

// TestCurrentSessionKeyNoSessionStillErrors verifies the fallback does not mask
// the genuine "not a session" case: outside a worktree, env unset, and no live
// implicit session, currentSessionKey still errors.
func TestCurrentSessionKeyNoSessionStillErrors(t *testing.T) {
	t.Setenv("SPINCLASS_SESSION_ID", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repo := t.TempDir() // no implicit session written
	t.Chdir(repo)

	if _, err := currentSessionKey(); err == nil {
		t.Fatal("expected error when no implicit session and not a worktree")
	}
}

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
