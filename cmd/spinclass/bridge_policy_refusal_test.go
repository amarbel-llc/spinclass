package main

// Every policy refusal of restart-this-session and close-this-session over the
// bridge is a refusal (-32001, "refused: ", outcome refused). A POLICY refusal is
// a rule saying no; an ERROR is something that failed (a hand-off that would not
// exec, an I/O error) and stays a tool error. The text names who can lift the
// rule and never a path the session itself has.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

func TestBridgedPolicyRefusalsAreRefusals(t *testing.T) {
	type tc struct {
		name  string
		setup func(t *testing.T, f liarFixture)
		run   func(f liarFixture) (string, bool)
		want  []string
	}
	restart := func(f liarFixture) (string, bool) {
		return runHandler(handleRestartThisSession)(bridgedCtxFor(f.a.key), json.RawMessage(`{}`))
	}
	closeIt := func(f liarFixture) (string, bool) {
		return runHandler(handleCloseThisSession)(bridgedCtxFor(f.a.key), json.RawMessage(`{}`))
	}
	cases := []tc{
		{
			name:  "restart over a running job",
			setup: func(t *testing.T, f liarFixture) { writeRunningJob(t, f.a.wt) },
			run:   restart,
			want:  []string{"a job is still running", `"restart" on this session`, "sc restart-child-session", "--force", "always asked of the human"},
		},
		{
			name:  "close over a running job",
			setup: func(t *testing.T, f liarFixture) { writeRunningJob(t, f.a.wt) },
			run:   closeIt,
			want:  []string{"a job is still running", "outside the unit"},
		},
		{
			name: "close that would orphan a session",
			setup: func(t *testing.T, f liarFixture) {
				if err := updateLoadedSession(f.b.record(t), func(cur *session.State) bool {
					cur.Holders = []string{f.a.principal}
					return true
				}); err != nil {
					t.Fatal(err)
				}
			},
			run:  closeIt,
			want: []string{"no accepted holder", "outside the unit", "sc grant-session-handle"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newLiarFixture(t)
			c.setup(t, f)
			text, isErr := c.run(f)
			if !isErr || !strings.HasPrefix(text, "refused: ") {
				t.Fatalf("got isErr=%v %q, want a refused: text", isErr, text)
			}
			for _, w := range c.want {
				if !strings.Contains(text, w) {
					t.Errorf("the refusal lacks %q: %q", w, text)
				}
			}
			for _, bad := range []string{"restart with force", "ask the operator", f.b.key, f.b.principal} {
				if strings.Contains(text, bad) {
					t.Errorf("the refusal carries %q: %q", bad, text)
				}
			}
			if len(*f.handoff) != 0 || f.a.record(t).RestartRequest != nil || f.a.record(t).CloseRequest != nil {
				t.Error("something was queued or run")
			}
		})
	}
}

// The posture and widening rules are refused in the contract's text too (a
// bridged restart-this-session never reaches them, since it refuses any posture
// first; restart-child-session shares them), and unchanged off the bridge.
func TestPolicyRefusalOfPostureRulesCarriesBridgeText(t *testing.T) {
	f := newLiarFixture(t)
	a := f.a.record(t)
	ask := func(posture string) error {
		_, err := queueRestartPosture(a, f.a.principal, f.a.principal, "r", false, posture)
		return err
	}
	bridged := bridgedCtxFor(f.a.key)
	check := func(name string, err error, want ...string) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: no refusal", name)
		}
		res := bridgeRefusalResult(bridged, err)
		if !res.IsErr || !strings.HasPrefix(res.Text, "refused: ") {
			t.Errorf("%s: got %q, want a refused: text", name, res.Text)
		}
		for _, w := range want {
			if !strings.Contains(res.Text, w) {
				t.Errorf("%s: lacks %q: %q", name, w, res.Text)
			}
		}
		if off := bridgeRefusalResult(context.Background(), err); off.Text != err.Error() || strings.HasPrefix(off.Text, "refused: ") {
			t.Errorf("%s: off the bridge got %q, want %q", name, off.Text, err.Error())
		}
	}

	// No pinned entry.
	check("no pinned entry", ask("unattended"), "no pinned session entry", "outside the unit")

	// Pinned, widening not allowed by the operator.
	if err := updateLoadedSession(a, func(cur *session.State) bool {
		cur.Entry = harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	check("widening not allowed", ask("attended"), "allow-posture-widening", "the operator sets it in the global sweatfile")

	// Widening allowed: a session still may not widen itself.
	writeGlobalSweatfile(t, os.Getenv("HOME"), restartPostureHandoffTOML+allowWideningTOML)
	check("self-widening", ask("attended"), `"restart" on this session`, "outside the unit")
	if len(*f.handoff) != 0 {
		t.Errorf("a hand-off ran: %+v", *f.handoff)
	}
}

// A failure that is not a rule stays an error: a hand-off that will not exec is
// not a refusal.
func TestBridgedHandoffFailureIsAnErrorNotARefusal(t *testing.T) {
	f := newLiarFixture(t)
	stubRestartHandoff(t, "no such file", errors.New("exec failed"))
	text, isErr := runHandler(handleRestartThisSession)(bridgedCtxFor(f.a.key), json.RawMessage(`{}`))
	if !isErr || strings.HasPrefix(text, "refused: ") || !strings.Contains(text, "hand-off") {
		t.Errorf("got isErr=%v %q, want a plain error naming the hand-off", isErr, text)
	}
}

// Off the bridge the policy texts are what they were.
func TestPolicyRefusalTextsAreUnchangedOffTheBridge(t *testing.T) {
	_, wtPath := restartSelfFixture(t, true)
	stubRestartHandoff(t, "", nil)
	writeRunningJob(t, wtPath)
	text, isErr := callRestartThis(t, `{}`)
	if !isErr || strings.HasPrefix(text, "refused: ") || !strings.Contains(text, "is still running in worker/kid; a restart would kill it, so let it finish or cancel it first (or restart with force, which asks the operator)") {
		t.Errorf("restart over a job off the bridge: %v %q", isErr, text)
	}
	text, isErr = callCloseThis(t, `{"force":true}`)
	if !isErr || strings.HasPrefix(text, "refused: ") || text != "close-this-session takes no force argument and never discards work; only the operator can, with `sc close --force`" {
		t.Errorf("close force off the bridge: %v %q", isErr, text)
	}
}
