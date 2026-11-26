package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/dewey/pkgs/mesa"

	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// askedSession is a live session (this process is its PID) with a worktree.
func askedSession(t *testing.T, principal string) *session.State {
	t.Helper()
	return &session.State{
		SessionState: session.StateActive, PID: os.Getpid(), WorktreePath: t.TempDir(),
		Branch: "b", SessionKey: "repo/b", Principal: principal, StartedAt: time.Now(),
	}
}

func stubBridgeAsked(t *testing.T, asked, result string) {
	t.Helper()
	old := bridgeAskedRead
	bridgeAskedRead = func(*session.State) (string, string) { return asked, result }
	t.Cleanup(func() { bridgeAskedRead = old })
}

func TestABridgeAskedForAndOffIsSaidWithClownsReason(t *testing.T) {
	stateHome := t.TempDir()
	stubBridgeAsked(t, "required", "off: the serve created no bridge socket in 10s (is [confinement] bridge on for this host?)")
	got := bridgeAskedNote(askedSession(t, "p"), stateHome)
	want := "bridge: asked for (required), not running: the serve created no bridge socket in 10s (is [confinement] bridge on for this host?)"
	if got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
}

func TestClownsReasonIsBoundedAndControlFree(t *testing.T) {
	stubBridgeAsked(t, "offered", "off: bad\x1b[31m red\nnew line \xe2\x80\xae"+strings.Repeat("x", 600))
	got := bridgeAskedNote(askedSession(t, "p"), t.TempDir())
	for _, r := range got {
		if r < ' ' || r == 0x7f || r == 0x202e {
			t.Fatalf("the note carries %U: %q", r, got)
		}
	}
	if len([]rune(got)) > 420 {
		t.Errorf("the note is %d runes", len([]rune(got)))
	}
}

func TestARecordedBridgeWithNoStateFromTheServeIsSaid(t *testing.T) {
	stateHome := t.TempDir()
	stubBridgeAsked(t, "offered", "recorded")
	st := askedSession(t, "p")
	if got := bridgeAskedNote(st, stateHome); !strings.Contains(got, "asked for (offered), not running: the serve has reported no bridge state") {
		t.Errorf("no state file: note = %q", got)
	}
	if err := crossinglog.WriteState(crossinglog.StateDir(stateHome), "p", crossinglog.State{Session: "repo/b", Listening: true, Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := bridgeAskedNote(st, stateHome); got != "" {
		t.Errorf("a state file exists: note = %q, want nothing (a down state is the state file's to say)", got)
	}
	// No principal yet (a launch that never said hello): nothing can be claimed.
	if got := bridgeAskedNote(askedSession(t, ""), stateHome); got != "" {
		t.Errorf("a session with no principal: note = %q", got)
	}
}

func TestNothingIsSaidWhenTheSpecDidNotAskOrClownIsSilentOrTheSessionIsNotRunning(t *testing.T) {
	stateHome := t.TempDir()
	cases := map[string]struct {
		asked, result string
		st            func() *session.State
	}{
		"the spec did not ask":            {"", "off: whatever", func() *session.State { return askedSession(t, "p") }},
		"a clown that predates the field": {"offered", "", func() *session.State { return askedSession(t, "p") }},
		"a session that is not running": {"offered", "off: x", func() *session.State {
			s := askedSession(t, "p")
			s.SessionState = session.StateInactive
			return s
		}},
	}
	for name, c := range cases {
		stubBridgeAsked(t, c.asked, c.result)
		if got := bridgeAskedNote(c.st(), stateHome); got != "" {
			t.Errorf("%s: note = %q, want nothing", name, got)
		}
	}
	if got := bridgeAskedNote(nil, stateHome); got != "" {
		t.Errorf("nil session: %q", got)
	}
}

func TestTheAskedNoteReachesScListAndSessionHealth(t *testing.T) {
	signalEnv(t)
	stubBridgeAsked(t, "required", "off: clown refused the bridge: no usable runtime directory")
	live := askedSession(t, "p-9")
	if got := productionHealthDeps().Bridge(live); !strings.Contains(got, "bridge: asked for (required), not running: clown refused the bridge") {
		t.Errorf("session-health note = %q", got)
	}

	for name, width := range map[string]int{"plain": 0, "tty": 140} {
		opt := mesa.ForcePlain()
		if width > 0 {
			opt = mesa.ForceStyle()
		}
		list := renderListTable([]session.State{{
			SessionState: session.StateActive, PID: os.Getpid(), WorktreePath: t.TempDir(), Branch: "b",
			SessionKey: "repo/b", Principal: "p-9", StartedAt: time.Now(),
		}}, nil, nil, false, width, opt)
		if !strings.Contains(list, "asked for (required), not running") {
			t.Errorf("%s list does not show the missing bridge:\n%s", name, list)
		}
	}
}

func TestTheAskedReadFindsTheSpecAndClownsResultInTheStateDirectory(t *testing.T) {
	stateHome := signalEnv(t)
	wt := t.TempDir()
	st := &session.State{WorktreePath: wt}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A spec that does not ask reads as nothing, whatever the result says.
	write(confinement.UnitSpecPath(stateHome, wt), `{"version":1}`)
	if asked, result := bridgeAskedRead(st); asked != "" || result != "" {
		t.Errorf("a spec with no bridge field: asked %q result %q", asked, result)
	}
	write(confinement.UnitSpecPath(stateHome, wt), `{"version":1,"bridge":"required"}`)
	if asked, result := bridgeAskedRead(st); asked != "required" || result != "" {
		t.Errorf("a spec that asks and no record yet: asked %q result %q", asked, result)
	}
	write(confinement.UnitRecordPath(stateHome, wt), `{"version":1,"unit":"clown-agent-x.service","cgroup":"/user.slice/clown-agent.slice/clown-agent-x.service","bridge":"off: no socket"}`)
	if asked, result := bridgeAskedRead(st); asked != "required" || result != "off: no socket" {
		t.Errorf("spec and record: asked %q result %q", asked, result)
	}
	// A value outside the two the spec can carry asks for nothing.
	write(confinement.UnitSpecPath(stateHome, wt), `{"version":1,"bridge":"on"}`)
	if asked, _ := bridgeAskedRead(st); asked != "" {
		t.Errorf("an unknown spec value was taken as asking: %q", asked)
	}
}
