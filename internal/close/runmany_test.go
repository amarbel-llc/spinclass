package close

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// makeSession creates a worktree on repoPath with tracked session state, so
// resolveTarget can find it by branch name.
func makeSession(t *testing.T, repoPath, branch string) string {
	t.Helper()
	wtPath := filepath.Join(repoPath, ".worktrees", branch)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, branch)
	if err := session.Write(session.State{
		SessionState: session.StateInactive,
		RepoPath:     repoPath,
		WorktreePath: wtPath,
		Branch:       branch,
		SessionKey:   filepath.Base(repoPath) + "/" + branch,
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return wtPath
}

// Several targets close in one invocation, each as its own subtest under a
// single plan. Concatenating N single-target TAP documents would not be valid
// TAP, which is why RunResolved's document ownership had to move to the caller.
func TestRunManyClosesEachTargetAsSubtest(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repoPath := filepath.Join(t.TempDir(), "repo")
	testgit.MustInit(t, repoPath)

	wtA := makeSession(t, repoPath, "alpha")
	wtB := makeSession(t, repoPath, "bravo")

	var buf bytes.Buffer
	if err := RunMany(&buf, []string{"alpha", "bravo"}, true, boolPtr(false), "tap", nil); err != nil {
		t.Fatalf("RunMany: %v", err)
	}

	for _, wt := range []string{wtA, wtB} {
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Errorf("worktree %s should be removed", wt)
		}
	}

	out := buf.String()
	if n := strings.Count(out, "TAP version 14"); n != 1 {
		t.Errorf("want exactly one TAP document, got %d version lines:\n%s", n, out)
	}
	for _, want := range []string{"# Subtest: alpha", "# Subtest: bravo", "ok 1 - alpha", "ok 2 - bravo", "1..2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// One target failing must not abandon the rest — the remaining sessions still
// close — and the invocation must still report failure rather than exiting 0
// with sessions left alive.
func TestRunManyContinuesPastFailureAndReportsIt(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repoPath := filepath.Join(t.TempDir(), "repo")
	testgit.MustInit(t, repoPath)

	wtB := makeSession(t, repoPath, "bravo")

	var buf bytes.Buffer
	// "nope" resolves to nothing; "bravo" is real and must still be closed.
	err := RunMany(&buf, []string{"nope", "bravo"}, true, boolPtr(false), "tap", nil)
	if err == nil {
		t.Fatal("an unresolvable target must make the invocation fail")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error should name the failed target, got %q", err)
	}

	if _, statErr := os.Stat(wtB); !os.IsNotExist(statErr) {
		t.Error("a later target must still be closed after an earlier one fails")
	}
	out := buf.String()
	if !strings.Contains(out, "not ok 1 - nope") {
		t.Errorf("failed target must report not ok:\n%s", out)
	}
	if !strings.Contains(out, "ok 2 - bravo") {
		t.Errorf("succeeding target must report ok:\n%s", out)
	}
}

// Zero and one target delegate to Run, so the cwd/picker path and the
// single-target output shape are untouched — no subtest wrapper, which is what
// keeps the existing bats expectations valid.
func TestRunManySingleTargetKeepsFlatOutput(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repoPath := filepath.Join(t.TempDir(), "repo")
	testgit.MustInit(t, repoPath)
	wt := makeSession(t, repoPath, "solo")

	var buf bytes.Buffer
	if err := RunMany(&buf, []string{"solo"}, true, boolPtr(false), "tap", nil); err != nil {
		t.Fatalf("RunMany: %v", err)
	}

	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("worktree should be removed")
	}
	out := buf.String()
	if strings.Contains(out, "# Subtest:") {
		t.Errorf("a single target must not be wrapped in a subtest:\n%s", out)
	}
	if !strings.Contains(out, "ok 1 - close solo") {
		t.Errorf("output missing the flat close point:\n%s", out)
	}
}

func boolPtr(b bool) *bool { return &b }
