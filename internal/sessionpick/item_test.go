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
	it := ItemForState(s, now)
	if got := it.Title(); got != "fix login bug" {
		t.Errorf("Title() = %q, want %q", got, "fix login bug")
	}
}

func TestItemTitleFallsBackToWorktreeDirName(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	s := fixture(t, "")
	it := ItemForState(s, now)
	if got := it.Title(); got != "feature" {
		t.Errorf("Title() = %q, want %q (base of WorktreePath)", got, "feature")
	}
}

func TestItemDescriptionFormat(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	s := fixture(t, "fix login bug")
	// StartedAt 10:00, now 12:00 → "2h ago"; ExitedAt nil → StartedAt used.
	it := ItemForState(s, now)
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
	it := ItemForState(s, now)
	want := "inactive · 5m ago · @feature · myrepo"
	if got := it.Description(); got != want {
		t.Errorf("Description() = %q, want %q", got, want)
	}
}

func TestItemFilterValue(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	s := fixture(t, "fix login bug")
	it := ItemForState(s, now)
	got := it.FilterValue()
	for _, want := range []string{"fix login bug", "feature", "myrepo"} {
		if !strings.Contains(got, want) {
			t.Errorf("FilterValue() = %q, missing %q", got, want)
		}
	}
}

// TestItemForRemoteRow: remote picker rows built from cached ListRow
// fixtures. Title is the description falling back to the id, Detail is
// the fixed "remote(<name>) · <state> · cached" shape, Filter includes
// the host:id target, State stays nil (selection yields Target).
func TestItemForRemoteRow(t *testing.T) {
	cases := []struct {
		name       string
		host       string
		row        session.ListRow
		wantTitle  string
		wantDetail string
		wantTarget string
	}{
		{
			name:       "description as title",
			host:       "devbox",
			row:        session.ListRow{ID: "crisp-catalpa", State: "active", Description: "fix login bug", Repo: "spinclass"},
			wantTitle:  "fix login bug",
			wantDetail: "remote(devbox) · active · cached",
			wantTarget: "devbox:crisp-catalpa",
		},
		{
			name:       "title falls back to id",
			host:       "lab",
			row:        session.ListRow{ID: "molten-mango", State: "inactive", Repo: "clown"},
			wantTitle:  "molten-mango",
			wantDetail: "remote(lab) · inactive · cached",
			wantTarget: "lab:molten-mango",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := ItemForRemoteRow(tc.host, tc.row)
			if got := it.Title(); got != tc.wantTitle {
				t.Errorf("Title() = %q, want %q", got, tc.wantTitle)
			}
			if got := it.Description(); got != tc.wantDetail {
				t.Errorf("Description() = %q, want %q", got, tc.wantDetail)
			}
			if it.Target != tc.wantTarget {
				t.Errorf("Target = %q, want %q", it.Target, tc.wantTarget)
			}
			if it.State != nil {
				t.Errorf("State = %+v, want nil for remote rows", it.State)
			}
			if !strings.Contains(it.FilterValue(), tc.wantTarget) {
				t.Errorf("FilterValue() = %q, missing target %q", it.FilterValue(), tc.wantTarget)
			}
		})
	}
}

// TestItemsForRemoteRowsSortedByHost: the per-remote cache map flattens
// deterministically — hosts sorted by name, rows kept in cache order
// within a host.
func TestItemsForRemoteRowsSortedByHost(t *testing.T) {
	caches := map[string][]session.ListRow{
		"lab": {
			{ID: "zz-last", State: "inactive"},
		},
		"devbox": {
			{ID: "crisp-catalpa", State: "active"},
			{ID: "molten-mango", State: "inactive"},
		},
	}
	items := ItemsForRemoteRows(caches)
	want := []string{"devbox:crisp-catalpa", "devbox:molten-mango", "lab:zz-last"}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(items), len(want), items)
	}
	for i, target := range want {
		if items[i].Target != target {
			t.Errorf("items[%d].Target = %q, want %q", i, items[i].Target, target)
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
			if got := FormatRelDate(tc.t, now); got != tc.want {
				t.Errorf("FormatRelDate = %q, want %q", got, tc.want)
			}
		})
	}
}
