package sessionpick

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
)

// fixture returns a State whose worktree dir exists (so ResolveState
// doesn't flip to abandoned) in a deterministic non-active state.
func fixture(t *testing.T, desc string) session.State {
	t.Helper()
	wt := filepath.Join(t.TempDir(), ".worktrees", "feature")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	return session.State{
		SessionState: session.StateInactive,
		RepoPath:     "/home/u/repos/myrepo",
		WorktreePath: wt,
		Branch:       "feature",
		SessionKey:   "myrepo/feature",
		Description:  desc,
		StartedAt:    time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
	}
}

func TestItemTitleFromDescription(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	s := fixture(t, "fix login bug")
	it := itemForState(s, now)
	if got := it.Title(); got != "fix login bug" {
		t.Errorf("Title() = %q, want %q", got, "fix login bug")
	}
}

func TestItemTitleFallsBackToWorktreeDirName(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	s := fixture(t, "")
	it := itemForState(s, now)
	if got := it.Title(); got != "feature" {
		t.Errorf("Title() = %q, want %q (base of WorktreePath)", got, "feature")
	}
}

func TestItemDescriptionFormat(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	s := fixture(t, "fix login bug")
	// StartedAt 10:00, now 12:00 → "2h ago"; ExitedAt nil → StartedAt used.
	it := itemForState(s, now)
	want := "inactive · 2h ago · @feature · myrepo"
	if got := it.Description(); got != want {
		t.Errorf("Description() = %q, want %q", got, want)
	}
}

func TestItemDescriptionPrefersExitedAt(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	s := fixture(t, "fix login bug")
	exited := time.Date(2026, 6, 6, 11, 55, 0, 0, time.UTC) // 5m before now
	s.ExitedAt = &exited
	it := itemForState(s, now)
	want := "inactive · 5m ago · @feature · myrepo"
	if got := it.Description(); got != want {
		t.Errorf("Description() = %q, want %q", got, want)
	}
}

func TestItemFilterValue(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	s := fixture(t, "fix login bug")
	it := itemForState(s, now)
	got := it.FilterValue()
	for _, want := range []string{"fix login bug", "feature", "myrepo"} {
		if !strings.Contains(got, want) {
			t.Errorf("FilterValue() = %q, missing %q", got, want)
		}
	}
}

func TestFormatRelDateTiers(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"just now", now.Add(-30 * time.Second), "just now"},
		{"minutes", now.Add(-5 * time.Minute), "5m ago"},
		{"hours", now.Add(-3 * time.Hour), "3h ago"},
		{"days", now.Add(-2 * 24 * time.Hour), "2d ago"},
		{"date beyond a week", now.Add(-8 * 24 * time.Hour), "2026-05-29"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatRelDate(tc.t, now); got != tc.want {
				t.Errorf("formatRelDate = %q, want %q", got, tc.want)
			}
		})
	}
}
