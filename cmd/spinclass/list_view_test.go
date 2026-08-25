package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"code.linenisgreat.com/purse-first/libs/dewey/pkgs/mesa"
	"code.linenisgreat.com/spinclass/internal/session"
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

	// width 0 ⇒ content-sized layout (no truncation), so the spawned-by hint
	// stays contiguous in its cell for the substring assertion below.
	out := renderListTable(states, remoteRows, diags, false, 0, mesa.ForceStyle())

	for _, want := range []string{
		"ID", "STATUS", "AGE", "DESCRIPTION", // headers (REPO+NAME merged → ID)
		"spinclass/bright-cedar", "charm render", // active local row (ID == full session key)
		"spinclass/spawned-walnut",                    // spawned local row
		"↰spinclass/bright-cedar",                     // spawned-by hint (contiguous in cell)
		"devbox:spinclass/crisp-catalpa", "fix login", // remote row (host:repo/id in one cell)
		"lab: unreachable", // diagnostic
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ghost") {
		t.Errorf("abandoned row must be filtered when closed=false:\n%s", out)
	}
}

// TestRenderListTableFoldsClownCountIntoStatus: when clown presence exists for
// a session's key (decoration), each live clown is rendered as a 🤡 inside the
// merged STATUS cell — there is no longer a separate CLOWNS column.
func TestRenderListTableFoldsClownCountIntoStatus(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "clown", "presence")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Two live clowns on the same session key → two presence records → two 🤡.
	for _, ch := range []string{"abcd", "efgh"} {
		rec := `{"sessionKey":"uuid-` + ch + `","channelId":"` + ch +
			`","decoration":"spinclass/feat","lastSeen":"` +
			time.Now().Format(time.RFC3339Nano) + `"}`
		if err := os.WriteFile(filepath.Join(dir, ch+".json"), []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	out := renderListTable([]session.State{{
		SessionState: session.StateActive,
		PID:          os.Getpid(),
		WorktreePath: t.TempDir(),
		Branch:       "feat",
		SessionKey:   "spinclass/feat",
		StartedAt:    time.Now(),
	}}, nil, nil, false, 0, mesa.ForceStyle())

	if strings.Contains(out, "CLOWNS") {
		t.Errorf("CLOWNS column was merged into STATUS; header must be gone:\n%s", out)
	}
	// One 🤡 per clown (not a 🤡N count): two clowns must render two emoji.
	if !strings.Contains(out, "🤡🤡") {
		t.Errorf("expected two 🤡 (one per clown) in the STATUS cell:\n%s", out)
	}
}

// TestRenderListTablePresenceFoldsIntoStatus: a session whose recorded PID is
// dead (base state inactive) but which has a live clown must still surface that
// clown in its STATUS cell — the #153 concern that the status never contradict
// the clown count beside it. With the dot-only STATUS design the
// resolved state (inactive→running-detached) is carried by the dot's COLOR,
// which lipgloss strips in a non-TTY test, so the assertable signal here is the
// 🤡 badge; the inactive→running-detached promotion itself is covered by
// session.TestResolveDisplayState.
func TestRenderListTablePresenceFoldsIntoStatus(t *testing.T) {
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
	}}, nil, nil, false, 0, mesa.ForceStyle())

	if !strings.Contains(out, "🤡") {
		t.Errorf("live clown over dead PID must surface a 🤡 in STATUS:\n%s", out)
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
	}, nil, nil, true, 0, mesa.ForceStyle())

	if !strings.Contains(out, "ghost") {
		t.Errorf("closed=true must include abandoned row:\n%s", out)
	}
}

// TestRenderListTableEmpty checks the empty-state message and that diagnostics
// still render when there are no session rows.
func TestRenderListTableEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // hermetic: no clown presence
	out := renderListTable(nil, nil, []string{"lab: unreachable (timeout)"}, false, 0, mesa.ForceStyle())
	if !strings.Contains(out, "No sessions.") {
		t.Errorf("empty list should say 'No sessions.':\n%s", out)
	}
	if !strings.Contains(out, "lab: unreachable") {
		t.Errorf("empty list should still show diagnostics:\n%s", out)
	}
}

// TestRenderListTableTruncatesDescription verifies the DESCRIPTION column is
// bounded to the terminal width instead of overflowing and breaking the
// grid: given a known width, no rendered line exceeds it, and an overlong
// description is ellipsized (mesa truncates a Flex column to one line rather
// than wrapping it — RFC 0003 §7.2) so its trailing END marker does NOT
// survive, but the truncation marker does.
func TestRenderListTableTruncatesDescription(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // hermetic: no clown presence
	const width = 100
	longDesc := strings.Repeat("alpha bravo charlie delta ", 12) + "END"

	out := renderListTable([]session.State{{
		SessionState: session.StateActive,
		PID:          os.Getpid(),
		WorktreePath: t.TempDir(),
		Branch:       "wide-willow",
		SessionKey:   "spinclass/wide-willow",
		Description:  longDesc,
		StartedAt:    time.Now(),
	}}, nil, nil, false, width, mesa.ForceStyle())

	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line exceeds width %d (got %d): %q", width, w, line)
		}
	}
	if strings.Contains(out, "END") {
		t.Errorf("overlong description must be truncated, not preserved in full:\n%s", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("truncated description must carry the ellipsis marker:\n%s", out)
	}
}
