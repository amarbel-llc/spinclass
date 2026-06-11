package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/attestation"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/testgit"
)

// TestResolveGatedSession exercises the resolved/reject outcomes of the
// extracted merge/check session-gate preamble: a worktree session
// (ok=true, gitErr=nil, implicit=false, repoPath/branch from git), a live
// implicit main-checkout session (ok=true, gitErr=nil, implicit=true,
// repoPath/branch from the state file), and a bare dir that is neither
// (ok=false with the canonical reject message). All three run with the
// attestation gate dormant — no [[pre-merge-skills]] in the loaded hierarchy —
// so the gate passes and the routing identity is what's under test.
//
// The third outcome — git-fail (ok=true, gitErr set, gs zero) — is not
// exercised here: it requires a cwd where worktree.IsWorktree is true but
// git.CommonDir/BranchCurrent then fails, which means a deliberately corrupt
// .git pointer that's brittle to construct. The merge handlers' fatality on
// gitErr is the only consumer of that outcome and is covered by the live merge
// path; the helper just forwards git's error verbatim.
//
// The gate's dormancy is load-bearing and non-obvious here: the sweatfile
// cascade walks the repo dir's ancestors up to $HOME, and $TMPDIR (and thus
// t.TempDir) points *inside* this session's worktree under ~/eng, whose
// sweatfile declares [[pre-merge-skills]]. To keep the gate dormant each subtest
// pins $HOME to the repo's own parent dir, so chainAncestors stops immediately
// and never reaches ~/eng — independent of where t.TempDir lands.
func TestResolveGatedSession(t *testing.T) {
	t.Run("worktree", func(t *testing.T) {
		testgit.RequireGit(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		base := t.TempDir()
		t.Setenv("HOME", base) // bound the cascade walk at the repo's parent
		repo := filepath.Join(base, "repo")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatalf("mkdir repo: %v", err)
		}
		testgit.MustInit(t, repo)
		wt := filepath.Join(repo, ".worktrees", "feature")
		testgit.MustWorktreeAdd(t, repo, wt, "feature")
		// resolveGatedSession reads the sweatfile hierarchy from cwd; chdir into
		// the worktree so the dormant-gate path resolves there.
		t.Chdir(wt)

		gs, gitErr, failMsg, ok := resolveGatedSession(wt)
		if !ok {
			t.Fatalf("expected ok, got reject: %q", failMsg)
		}
		if gitErr != nil {
			t.Errorf("worktree session: unexpected gitErr: %v", gitErr)
		}
		if gs.implicit {
			t.Errorf("worktree session: implicit = true, want false")
		}
		if gs.repoPath == "" {
			t.Errorf("worktree session: repoPath empty, want git-common-dir")
		}
		if gs.branch != "feature" {
			t.Errorf("worktree session: branch = %q, want feature", gs.branch)
		}
	})

	t.Run("implicit", func(t *testing.T) {
		testgit.RequireGit(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		base := t.TempDir()
		t.Setenv("HOME", base) // dormant gate — see the TestResolveGatedSession doc comment
		// `git init` (not worktree add): .git is a DIRECTORY, so
		// worktree.IsWorktree is false and the implicit lookup fires — while
		// git.CommonDir still resolves to this repo (not the enclosing session
		// worktree TMPDIR lands in), keeping the cascade bounded by base/HOME.
		repo := filepath.Join(base, "repo")
		testgit.MustInit(t, repo)
		st := session.State{
			Kind:         session.KindImplicit,
			PID:          os.Getpid(), // alive: this test process
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

		gs, gitErr, failMsg, ok := resolveGatedSession(repo)
		if !ok {
			t.Fatalf("expected ok, got reject: %q", failMsg)
		}
		if gitErr != nil {
			t.Errorf("implicit session: unexpected gitErr: %v", gitErr)
		}
		if !gs.implicit {
			t.Errorf("implicit session: implicit = false, want true")
		}
		if gs.repoPath != repo {
			t.Errorf("implicit session: repoPath = %q, want %q", gs.repoPath, repo)
		}
		if gs.branch != "master" {
			t.Errorf("implicit session: branch = %q, want master", gs.branch)
		}
	})

	t.Run("reject", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		// Bare dir: not a worktree, no implicit session written. No $HOME pin is
		// needed here (unlike the other subtests) — the reject fires before the
		// attestation gate, so the sweatfile cascade that ~/eng's
		// [[pre-merge-skills]] would taint is never reached on this path.
		dir := t.TempDir()
		t.Chdir(dir)

		gs, gitErr, failMsg, ok := resolveGatedSession(dir)
		if ok {
			t.Fatalf("expected reject, got ok (gs=%+v)", gs)
		}
		if gitErr != nil {
			t.Errorf("reject: unexpected gitErr: %v", gitErr)
		}
		if failMsg != "not inside a worktree session" {
			t.Errorf("reject message = %q, want %q", failMsg, "not inside a worktree session")
		}
	})
}

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

// TestHandleUpdateDescriptionImplicit verifies that
// update-this-session-description falls back to a live implicit
// (main-checkout) session when not inside a worktree, writing the description
// into its state-<rand>.json — the behavior FDR 0014 documents (#137).
func TestHandleUpdateDescriptionImplicit(t *testing.T) {
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

	res, err := handleUpdateDescription(context.Background(), json.RawMessage(`{"description":"triage the flaky test"}`), nil)
	if err != nil {
		t.Fatalf("handleUpdateDescription: %v", err)
	}
	if res.IsErr {
		t.Fatalf("expected success for implicit session, got error result: %s", res.Text)
	}

	got, _, ferr := session.FindImplicitAtCwd(repo)
	if ferr != nil || got == nil {
		t.Fatalf("FindImplicitAtCwd after update: state=%v err=%v", got, ferr)
	}
	if got.Description != "triage the flaky test" {
		t.Errorf("persisted description = %q, want %q", got.Description, "triage the flaky test")
	}
}

// TestHandleUpdateDescriptionNoSessionStillErrors verifies the fallback does
// not mask the genuine "not a session" case: outside a worktree with no live
// implicit session, the tool still returns the canonical error.
func TestHandleUpdateDescriptionNoSessionStillErrors(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	dir := t.TempDir() // no implicit session written
	t.Chdir(dir)

	res, err := handleUpdateDescription(context.Background(), json.RawMessage(`{"description":"anything"}`), nil)
	if err != nil {
		t.Fatalf("handleUpdateDescription: %v", err)
	}
	if !res.IsErr {
		t.Fatalf("expected error result with no session, got success: %s", res.Text)
	}
	if !strings.Contains(res.Text, "not inside a worktree session") {
		t.Errorf("error text = %q, want it to contain %q", res.Text, "not inside a worktree session")
	}
}

// TestHandleChatListSessionsSpawnedByAnnotation verifies chat-list-sessions
// rows carry the spawn lineage hint (FDR 0006) as a `[spawned-by <key>]`
// annotation, mirroring the implicit-session `{branch}` hint's placement,
// and that non-spawned sessions render without it.
func TestHandleChatListSessionsSpawnedByAnnotation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base := t.TempDir()
	repo := filepath.Join(base, "spinclass")
	spawnedWT := filepath.Join(repo, ".worktrees", "spawned-walnut")
	plainWT := filepath.Join(repo, ".worktrees", "plain-pine")
	for _, dir := range []string{spawnedWT, plainWT} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	states := []session.State{
		{
			SessionState: session.StateInactive,
			RepoPath:     repo,
			WorktreePath: spawnedWT,
			Branch:       "spawned-walnut",
			SessionKey:   "spinclass/spawned-walnut",
			SpawnedBy:    "spinclass/bright-cedar",
			Description:  "fix the thing",
		},
		{
			SessionState: session.StateInactive,
			RepoPath:     repo,
			WorktreePath: plainWT,
			Branch:       "plain-pine",
			SessionKey:   "spinclass/plain-pine",
		},
	}
	for _, s := range states {
		if err := session.Write(s); err != nil {
			t.Fatalf("session.Write(%s): %v", s.Branch, err)
		}
	}

	res, err := handleChatListSessions(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("handleChatListSessions: %v", err)
	}
	if res.IsErr {
		t.Fatalf("unexpected error result: %s", res.Text)
	}

	var spawnedLine, plainLine string
	for _, line := range strings.Split(res.Text, "\n") {
		if strings.HasPrefix(line, "spinclass/spawned-walnut ") {
			spawnedLine = line
		}
		if strings.HasPrefix(line, "spinclass/plain-pine ") {
			plainLine = line
		}
	}
	if spawnedLine == "" || plainLine == "" {
		t.Fatalf("rows missing from output:\n%s", res.Text)
	}
	want := "{spawned-walnut} [spawned-by spinclass/bright-cedar] — fix the thing"
	if !strings.Contains(spawnedLine, want) {
		t.Errorf("spawned row = %q, want it to contain %q", spawnedLine, want)
	}
	if strings.Contains(plainLine, "[spawned-by") {
		t.Errorf("plain row unexpectedly annotated: %q", plainLine)
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
	if !strings.Contains(got, "plain verdict lines") {
		t.Errorf("description should advertise the plain verdict-line result shape (TAP is retired for merge/check):\n%s", got)
	}
	if strings.Contains(got, "TAP") {
		t.Errorf("description still claims TAP output:\n%s", got)
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
	if !strings.Contains(got, "plain verdict lines") {
		t.Errorf("description should advertise the plain verdict-line result shape (TAP is retired for merge/check):\n%s", got)
	}
	if strings.Contains(got, "TAP") || strings.Contains(got, "test point") {
		t.Errorf("description still claims TAP output:\n%s", got)
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
