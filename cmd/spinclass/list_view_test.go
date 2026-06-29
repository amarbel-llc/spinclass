package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
)

// TestListRenderMode locks the CLI render-mode decision: --watch wins over
// everything, explicit json is json on TTY or pipe, an unset --format is
// pretty only on a TTY (the new interactive default) and plain otherwise,
// and an explicit tap/table opts back out of the pretty table.
func TestListRenderMode(t *testing.T) {
	cases := []struct {
		name   string
		format string
		isTTY  bool
		watch  bool
		want   listMode
	}{
		{"watch beats json", "json", true, true, modeWatch},
		{"watch on non-tty still resolves watch", "", false, true, modeWatch},
		{"json explicit on tty", "json", true, false, modeJSON},
		{"json on pipe", "json", false, false, modeJSON},
		{"unset on tty is pretty", "", true, false, modePretty},
		{"unset on pipe is plain", "", false, false, modePlain},
		{"explicit tap on tty stays plain", "tap", true, false, modePlain},
		{"explicit table on tty stays plain", "table", true, false, modePlain},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := listRenderMode(c.format, c.isTTY, c.watch); got != c.want {
				t.Errorf("listRenderMode(%q, tty=%v, watch=%v) = %v, want %v",
					c.format, c.isTTY, c.watch, got, c.want)
			}
		})
	}
}

func TestParseWatchInterval(t *testing.T) {
	if d, err := parseWatchInterval(""); err != nil || d != 2*time.Second {
		t.Errorf("default: got %v, %v; want 2s, nil", d, err)
	}
	if d, err := parseWatchInterval("5s"); err != nil || d != 5*time.Second {
		t.Errorf("5s: got %v, %v", d, err)
	}
	for _, bad := range []string{"nope", "0s", "-3s"} {
		if _, err := parseWatchInterval(bad); err == nil {
			t.Errorf("parseWatchInterval(%q): expected error, got nil", bad)
		}
	}
}

// TestRenderListTable verifies the pretty table includes local rows, the
// spawned-by lineage hint, prefixed remote rows, and per-host diagnostics,
// and that an abandoned (missing-worktree) row is filtered when closed=false.
func TestRenderListTable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // hermetic: no clown presence
	wt := t.TempDir()                       // exists → ResolveState is not abandoned

	states := []session.State{
		{
			SessionState: session.StateActive,
			PID:          os.Getpid(), // alive → stays active
			RepoPath:     "/x/spinclass",
			WorktreePath: wt,
			Branch:       "bright-cedar",
			SessionKey:   "spinclass/bright-cedar",
			Description:  "charm render",
			StartedAt:    time.Now(),
		},
		{
			SessionState: session.StateInactive,
			RepoPath:     "/x/spinclass",
			WorktreePath: wt,
			Branch:       "spawned-walnut",
			SessionKey:   "spinclass/spawned-walnut",
			SpawnedBy:    "spinclass/bright-cedar",
			StartedAt:    time.Now(),
		},
		{
			// Missing worktree → ResolveState == abandoned → filtered when
			// closed is false.
			SessionState: session.StateActive,
			WorktreePath: filepath.Join(wt, "gone"),
			Branch:       "ghost",
			SessionKey:   "spinclass/ghost",
		},
	}
	remoteRows := []session.ListRow{
		{ID: "crisp-catalpa", State: "active", Description: "fix login", Repo: "spinclass", Remote: "devbox"},
	}
	diags := []string{"lab: unreachable (no route to host)"}

	out := renderListTable(states, remoteRows, diags, false)

	for _, want := range []string{
		"STATE", "SESSION", "BRANCH", "AGE", "DESCRIPTION", // headers
		"spinclass/bright-cedar", "charm render", // active local row
		"spinclass/spawned-walnut",          // spawned local row
		"↰spinclass/bright-cedar",           // spawned-by hint (contiguous in cell)
		"devbox:crisp-catalpa", "fix login", // prefixed remote row
		"lab: unreachable", // diagnostic
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "spinclass/ghost") {
		t.Errorf("abandoned row must be filtered when closed=false:\n%s", out)
	}
}

// TestRenderListTableShowsClownColumn: when clown presence exists for a
// session's key (decoration), the table gains a CLOWNS column.
func TestRenderListTableShowsClownColumn(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "clown", "presence")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := `{"sessionKey":"uuid","channelId":"abcd","decoration":"spinclass/feat","lastSeen":"` +
		time.Now().Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "abcd.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}

	out := renderListTable([]session.State{{
		SessionState: session.StateActive,
		PID:          os.Getpid(),
		WorktreePath: t.TempDir(),
		Branch:       "feat",
		SessionKey:   "spinclass/feat",
		StartedAt:    time.Now(),
	}}, nil, nil, false)

	if !strings.Contains(out, "CLOWNS") {
		t.Errorf("expected CLOWNS column when presence exists:\n%s", out)
	}
}

// TestRenderListTablePresenceOverridesInactiveState: a session whose recorded
// PID is dead (base state inactive) but which has a live clown must render
// running-detached, not inactive — the #153 fix, so the STATE cell never
// contradicts the CLOWNS count beside it.
func TestRenderListTablePresenceOverridesInactiveState(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "clown", "presence")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := `{"sessionKey":"uuid","channelId":"abcd","decoration":"spinclass/feat","lastSeen":"` +
		time.Now().Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "abcd.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}

	out := renderListTable([]session.State{{
		SessionState: session.StateInactive, // recorded PID dead → base inactive
		WorktreePath: t.TempDir(),           // worktree exists → not abandoned
		Branch:       "feat",
		SessionKey:   "spinclass/feat",
		StartedAt:    time.Now(),
	}}, nil, nil, false)

	if !strings.Contains(out, session.StateRunningDetached) {
		t.Errorf("live clown over dead PID must render running-detached:\n%s", out)
	}
}

// TestRenderListTableClosedIncludesAbandoned confirms the closed flag flips
// the abandoned-row filter on, mirroring the text path's --closed semantics.
func TestRenderListTableClosedIncludesAbandoned(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // hermetic: no clown presence
	out := renderListTable([]session.State{
		{
			SessionState: session.StateActive,
			WorktreePath: filepath.Join(t.TempDir(), "gone"), // missing → abandoned
			Branch:       "ghost",
			SessionKey:   "spinclass/ghost",
		},
	}, nil, nil, true)

	if !strings.Contains(out, "spinclass/ghost") {
		t.Errorf("closed=true must include abandoned row:\n%s", out)
	}
}

// TestRenderListTableEmpty checks the empty-state message and that diagnostics
// still render when there are no session rows.
func TestRenderListTableEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // hermetic: no clown presence
	out := renderListTable(nil, nil, []string{"lab: unreachable (timeout)"}, false)
	if !strings.Contains(out, "No sessions.") {
		t.Errorf("empty list should say 'No sessions.':\n%s", out)
	}
	if !strings.Contains(out, "lab: unreachable") {
		t.Errorf("empty list should still show diagnostics:\n%s", out)
	}
}
