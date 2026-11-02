package hooks

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testfs"
)

// moxyfileGuardFixture is a HOME with a session worktree four levels under
// it, so moxy's hierarchy walk (global, every parent between HOME and the
// cwd, the cwd) has several candidate moxyfiles on it and one off it.
type moxyfileGuardFixture struct {
	home, repo, worktree string
}

func newMoxyfileGuardFixture(t *testing.T) moxyfileGuardFixture {
	t.Helper()
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	wt := filepath.Join(repo, ".worktrees", "x")
	testfs.MustMkdirAll(t, filepath.Join(wt, "sub"), 0o755)
	testfs.MustMkdirAll(t, filepath.Join(home, ".config", "moxy"), 0o755)
	t.Setenv("HOME", home)
	setEnvOrUnset(t, "CLOWN_UNIT", "")
	return moxyfileGuardFixture{home: home, repo: repo, worktree: wt}
}

// decide runs one PreToolUse call for the fixture's session and returns the
// hook's decision and reason, or "" when the hook wrote nothing.
func (f moxyfileGuardFixture) decide(t *testing.T, mode, tool string, toolInput map[string]any) (string, string) {
	t.Helper()
	payload := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       tool,
		"tool_input":      toolInput,
		"cwd":             f.worktree,
	}
	if mode != "" {
		payload["permission_mode"] = mode
	}
	data, _ := json.Marshal(payload)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(data), &stdout, f.repo, f.worktree, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stdout.Len() == 0 {
		return "", ""
	}
	return parseHookDecision(t, stdout.Bytes())
}

// #438 slice 0: a write of any moxyfile on moxy's hierarchy walk for the
// session (the global one, every parent between HOME and the worktree, and
// the worktree's own) asks in an unconfined session, through every tool that
// can create, change, replace, move or remove one. The moxyfile decides which
// tools a session has, so a session must not widen it on its own.
func TestMoxyfileEditAsksInAnUnconfinedSession(t *testing.T) {
	f := newMoxyfileGuardFixture(t)
	const moxy = "mcp__plugin_moxy_moxy__"
	onWalk := []string{
		filepath.Join(f.home, ".config", "moxy", "moxyfile"),
		filepath.Join(f.home, "moxyfile"),
		filepath.Join(f.home, "work", "moxyfile"),
		filepath.Join(f.repo, "moxyfile"),
		filepath.Join(f.repo, ".worktrees", "moxyfile"),
		filepath.Join(f.worktree, "moxyfile"),
	}
	for _, target := range onWalk {
		for _, tc := range []struct {
			tool  string
			input map[string]any
		}{
			{"Write", map[string]any{"file_path": target, "content": "x"}},
			{"Edit", map[string]any{"file_path": target, "old_string": "a", "new_string": "b"}},
			{"MultiEdit", map[string]any{"file_path": target}},
			{moxy + "folio_write", map[string]any{"file_path": target, "content": "x"}},
			{moxy + "folio_cp", map[string]any{"source": filepath.Join(f.worktree, "a"), "destination": target}},
			{moxy + "folio_mv", map[string]any{"source": filepath.Join(f.worktree, "a"), "destination": target}},
			{moxy + "folio_mv", map[string]any{"source": target, "destination": filepath.Join(f.worktree, "a")}},
			{moxy + "folio_link", map[string]any{"source": filepath.Join(f.worktree, "a"), "target": target}},
			{moxy + "folio_rm", map[string]any{"paths": filepath.Join(f.worktree, "a") + " " + target}},
		} {
			d, r := f.decide(t, "", tc.tool, tc.input)
			if d != "ask" || !strings.Contains(r, "moxyfile") {
				t.Errorf("%s of %s: got %q (%s), want ask naming the moxyfile", tc.tool, target, d, r)
			}
		}
	}
}

// Reads never ask (an own-worktree folio_read is the #372 allow, a Read is
// nothing), and a file that is merely named moxyfile somewhere off the walk
// (a fixture under a subdirectory) or named like one is not a moxyfile: the
// guard is the hierarchy moxy merges, nothing wider.
func TestMoxyfileGuardLeavesReadsAndOffWalkFilesAlone(t *testing.T) {
	f := newMoxyfileGuardFixture(t)
	target := filepath.Join(f.worktree, "moxyfile")
	for _, tc := range []struct {
		tool  string
		input map[string]any
	}{
		{"Read", map[string]any{"file_path": target}},
		{"mcp__plugin_moxy_moxy__folio_read", map[string]any{"file_path": target}},
		{"Write", map[string]any{"file_path": filepath.Join(f.worktree, "sub", "moxyfile")}},
		{"Write", map[string]any{"file_path": filepath.Join(f.worktree, "moxyfile.bak")}},
		{"Write", map[string]any{"file_path": filepath.Join(f.worktree, "sub", "moxyfile.toml")}},
		{"mcp__plugin_moxy_moxy__folio_cp", map[string]any{"source": target, "destination": filepath.Join(f.worktree, "copy")}},
	} {
		if d, r := f.decide(t, "", tc.tool, tc.input); d == "ask" || d == "deny" {
			t.Errorf("%s %v: got %q (%s), want no ask", tc.tool, tc.input, d, r)
		}
	}
}

// A symlink to a moxyfile on the walk is the moxyfile: the guard judges the
// resolved path, as the sweatfile guard does.
func TestMoxyfileGuardResolvesSymlinks(t *testing.T) {
	f := newMoxyfileGuardFixture(t)
	real := filepath.Join(f.home, ".config", "moxy", "moxyfile")
	testfs.MustWriteFile(t, real, []byte(""), 0o644)
	link := filepath.Join(f.worktree, "sub", "cfg")
	testfs.MustSymlink(t, real, link)
	if d, _ := f.decide(t, "", "Write", map[string]any{"file_path": link, "content": "x"}); d != "ask" {
		t.Errorf("write through a symlink to the global moxyfile: got %q, want ask", d)
	}
}

// Under dontAsk the ask is the usual deny: an unattended unconfined session
// cannot widen its moxyfile at all.
func TestMoxyfileEditIsDeniedUnderDontAsk(t *testing.T) {
	f := newMoxyfileGuardFixture(t)
	d, r := f.decide(t, "dontAsk", "Write", map[string]any{"file_path": filepath.Join(f.worktree, "moxyfile"), "content": "x"})
	if d != "deny" || !strings.Contains(r, "moxyfile") || !strings.HasSuffix(r, dontAskSuffix) {
		t.Errorf("got %q (%s), want the moxyfile ask converted to a deny", d, r)
	}
}

// Inside a confined unit (CLOWN_UNIT set) the hook writes no decision for a
// moxyfile edit: the unit's permission rules decide, as for any other write
// there. The in-unit moxyfile is the profile's, and the unit bounds the
// worker, so a forced prompt would only strand an unattended worker.
func TestMoxyfileEditIsPermsBasedInAConfinedUnit(t *testing.T) {
	f := newMoxyfileGuardFixture(t)
	t.Setenv("CLOWN_UNIT", testUnit)
	for _, mode := range []string{"", "dontAsk"} {
		d, r := f.decide(t, mode, "Write", map[string]any{"file_path": filepath.Join(f.worktree, "moxyfile"), "content": "x"})
		if d != "" {
			t.Errorf("mode %q: got %q (%s), want no decision", mode, d, r)
		}
	}
}
