package main

import (
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
)

// TestResumeConfirmPlan tables the confirmation decision: which dialog
// (if any) `sc resume` shows before attaching, per
// docs/plans/2026-06-06-clown-style-resume-design.md decision 2.
func TestResumeConfirmPlan(t *testing.T) {
	cases := []struct {
		name           string
		resolvedState  string
		explicitTarget bool
		yes            bool
		isTTY          bool
		wantKind       resumeConfirmKind
		wantErr        string // substring of the error; "" means no error
	}{
		{
			name:          "explicit target skips dialog",
			resolvedState: session.StateInactive, explicitTarget: true,
			wantKind: resumeConfirmNone,
		},
		{
			name:          "explicit target skips dialog even when active",
			resolvedState: session.StateActive, explicitTarget: true,
			wantKind: resumeConfirmNone,
		},
		{
			name:          "explicit target skips dialog even non-TTY",
			resolvedState: session.StateActive, explicitTarget: true, isTTY: false,
			wantKind: resumeConfirmNone,
		},
		{
			name:          "auto-detect on TTY shows standard confirm",
			resolvedState: session.StateInactive, isTTY: true,
			wantKind: resumeConfirmStandard,
		},
		{
			name:          "auto-detect with -y skips standard confirm",
			resolvedState: session.StateInactive, yes: true, isTTY: true,
			wantKind: resumeConfirmNone,
		},
		{
			name:          "auto-detect with -y skips even non-TTY",
			resolvedState: session.StateInactive, yes: true, isTTY: false,
			wantKind: resumeConfirmNone,
		},
		{
			name:          "auto-detect non-TTY without -y errors with hint",
			resolvedState: session.StateInactive, isTTY: false,
			wantErr: "pass -y",
		},
		{
			name:          "active session warns on TTY",
			resolvedState: session.StateActive, isTTY: true,
			wantKind: resumeConfirmWarning,
		},
		{
			name:          "active session warns even with -y",
			resolvedState: session.StateActive, yes: true, isTTY: true,
			wantKind: resumeConfirmWarning,
		},
		{
			name:          "active session non-TTY always errors",
			resolvedState: session.StateActive, isTTY: false,
			wantErr: "interactive terminal",
		},
		{
			name:          "active session non-TTY errors even with -y",
			resolvedState: session.StateActive, yes: true, isTTY: false,
			wantErr: "interactive terminal",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, err := resumeConfirmPlan(tc.resolvedState, tc.explicitTarget, tc.yes, tc.isTTY)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil (kind=%d)", tc.wantErr, kind)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want contains %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if kind != tc.wantKind {
				t.Errorf("kind = %d, want %d", kind, tc.wantKind)
			}
		})
	}
}

func detailFixtureState() session.State {
	started := time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC)
	exited := time.Date(2026, 6, 6, 11, 55, 0, 0, time.UTC)
	return session.State{
		RepoPath:     "/home/u/repos/myrepo",
		WorktreePath: "/home/u/repos/myrepo/.worktrees/catalpa",
		Branch:       "feature",
		SessionKey:   "myrepo/feature",
		Description:  "fix login bug",
		StartedAt:    started,
		ExitedAt:     &exited,
	}
}

// TestBuildResumeDetail asserts the indented key: value detail block
// mirroring clown's buildResumeDesc layout.
func TestBuildResumeDetail(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	s := detailFixtureState()
	got := buildResumeDetail(&s, session.StateInactive, false, now)
	want := "" +
		"  id:        catalpa\n" +
		"  repo:      myrepo/feature\n" +
		"  state:     inactive\n" +
		"  last:      5m ago\n" +
		"  worktree:  /home/u/repos/myrepo/.worktrees/catalpa\n"
	if got != want {
		t.Errorf("buildResumeDetail =\n%q\nwant\n%q", got, want)
	}
}

// TestBuildResumeDetailWarningVariant: the active-elsewhere variant
// prepends a warning paragraph before the detail block.
func TestBuildResumeDetailWarningVariant(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	s := detailFixtureState()
	got := buildResumeDetail(&s, session.StateActive, true, now)
	if !strings.HasPrefix(got, "Warning:") {
		t.Errorf("warning variant should start with a warning paragraph, got %q", got)
	}
	for _, want := range []string{"attached elsewhere", "  state:     active\n", "  id:        catalpa\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("buildResumeDetail = %q, missing %q", got, want)
		}
	}
}

// TestResumeConfirmTitle: title uses the description, falling back to
// the worktree dir name, same as the picker rows.
func TestResumeConfirmTitle(t *testing.T) {
	s := detailFixtureState()
	if got := resumeConfirmTitle(&s); got != "fix login bug" {
		t.Errorf("resumeConfirmTitle = %q, want %q", got, "fix login bug")
	}
	s.Description = ""
	if got := resumeConfirmTitle(&s); got != "catalpa" {
		t.Errorf("resumeConfirmTitle fallback = %q, want %q", got, "catalpa")
	}
}
