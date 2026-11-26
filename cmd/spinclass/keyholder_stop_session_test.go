package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// `sc keyholder-stop SESSION` is the public per-session stop `sc session-key`
// names in its refusals (principle 6): it stops that one session's pair and no
// other, under the same terminal, journal and NOT MINE rules as --all.

func TestKeyholderStopSessionStopsThatPairAndLeavesTheOthers(t *testing.T) {
	fx := newStopFixture(t, "s/a", "s/b")
	stdout, _ := stubOperatorTerminal(t, true)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{Session: "s/a"}); err != nil {
		t.Fatal(err)
	}
	if agentActive(fx, "s/a") || fx.f.active["spinclass-keyholder-fibby-"+keyholder.Hash("s/a")+".service"] {
		t.Error("a unit of the named session is still active")
	}
	if _, err := os.Lstat(filepath.Join(fx.l.Base, keyholder.Hash("s/a"))); !os.IsNotExist(err) {
		t.Errorf("the named session's directory survived: %v", err)
	}
	if !agentActive(fx, "s/b") {
		t.Fatal("stopping one session stopped another session's pair")
	}
	if _, err := os.Lstat(filepath.Join(fx.l.Base, keyholder.Hash("s/b"))); err != nil {
		t.Errorf("another session's directory was touched: %v", err)
	}
	if strings.Count(stdout.String(), "stopped ") != 1 {
		t.Errorf("output:\n%s", stdout.String())
	}
	b, err := os.ReadFile(fx.journal)
	if err != nil {
		t.Fatal(err)
	}
	var rec keyholderJournalRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &rec); err != nil || rec.Session != "s/a" || rec.Action != "stop" || rec.Result != "stopped" {
		t.Errorf("journal = %s (%+v, %v), want one stop of s/a", b, rec, err)
	}
}

// A real stop is the operator's act: with no terminal nothing is stopped.
func TestKeyholderStopSessionRefusesWithoutATerminalAndStopsNothing(t *testing.T) {
	fx := newStopFixture(t, "s/a")
	stubOperatorTerminal(t, false)
	err := runKeyholderStop(context.Background(), keyholderStopParams{Session: "s/a"})
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("no terminal = %v, want a refusal naming the terminals", err)
	}
	if !agentActive(fx, "s/a") {
		t.Error("a pair was stopped without a terminal")
	}
	if _, serr := os.Stat(fx.journal); serr == nil {
		t.Error("a refused stop wrote a journal entry")
	}
}

func TestKeyholderStopNeedsExactlyOneOfAllAndASession(t *testing.T) {
	fx := newStopFixture(t, "s/a")
	stubOperatorTerminal(t, true)
	for name, p := range map[string]keyholderStopParams{
		"neither":  {},
		"both":     {All: true, Session: "s/a"},
		"dry both": {All: true, Session: "s/a", DryRun: true},
	} {
		err := runKeyholderStop(context.Background(), p)
		if err == nil || !strings.Contains(err.Error(), "--all") || !strings.Contains(err.Error(), "SESSION") {
			t.Errorf("%s = %v, want an error naming --all and SESSION", name, err)
		}
	}
	if !agentActive(fx, "s/a") {
		t.Error("a refused call stopped a pair")
	}
}

func TestKeyholderStopSessionWithNoPairSaysSoAndTouchesNothing(t *testing.T) {
	fx := newStopFixture(t, "s/a")
	stdout, _ := stubOperatorTerminal(t, true)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{Session: "s/none"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "no key holder pair for session s/none") {
		t.Errorf("output:\n%s", stdout.String())
	}
	if !agentActive(fx, "s/a") {
		t.Error("naming a session with no pair stopped another session's pair")
	}
}

// A session name that matches no record and is not written as a key is an
// error naming how to give the key, not a silent success.
func TestKeyholderStopSessionNameThatIsNeitherARecordNorAKeyIsAnError(t *testing.T) {
	newStopFixture(t, "s/a")
	stubOperatorTerminal(t, true)
	err := runKeyholderStop(context.Background(), keyholderStopParams{Session: "nobody"})
	if err == nil || !strings.Contains(err.Error(), "<repo>/<branch>") {
		t.Errorf("err = %v, want one saying to give the key as <repo>/<branch>", err)
	}
}

// Not this state root's: the pair of the named session is units with no
// directory here, and is left running unless --including-unowned says so.
func TestKeyholderStopSessionLeavesAnotherStateRootsPairRunningUnlessIncludingUnowned(t *testing.T) {
	fx := newStopFixture(t, "s/mine")
	foreignPair(t, fx, "s/foreign")
	_, stderr := stubOperatorTerminal(t, true)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{Session: "s/foreign"}); err != nil {
		t.Fatal(err)
	}
	if !agentActive(fx, "s/foreign") {
		t.Fatal("a pair another state root recorded was stopped by this one")
	}
	if !strings.Contains(stderr.String(), "--including-unowned") {
		t.Errorf("stderr does not say how to stop it:\n%s", stderr.String())
	}
	if !agentActive(fx, "s/mine") {
		t.Error("naming a foreign session stopped this root's own pair")
	}
	if err := runKeyholderStop(context.Background(), keyholderStopParams{Session: "s/foreign", IncludeUnowned: true}); err != nil {
		t.Fatal(err)
	}
	if agentActive(fx, "s/foreign") {
		t.Error("--including-unowned did not stop the named foreign pair")
	}
	if !agentActive(fx, "s/mine") {
		t.Error("stopping the foreign session stopped this root's own pair")
	}
}

// --dry-run lists only the named session's pair and stops nothing, with no
// terminal.
func TestKeyholderStopSessionDryRunListsOnlyThatPair(t *testing.T) {
	fx := newStopFixture(t, "s/a", "s/b")
	stdout, _ := stubOperatorTerminal(t, false)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{Session: "s/a", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, keyholder.Hash("s/a")) || strings.Contains(out, keyholder.Hash("s/b")) {
		t.Errorf("the dry run did not list exactly the named pair:\n%s", out)
	}
	if !agentActive(fx, "s/a") || !agentActive(fx, "s/b") {
		t.Error("--dry-run stopped a pair")
	}
}

// The command the refusals name must exist, be visible, and take a session.
func TestKeyholderStopAndResumeAreVisibleCommandsAndStopTakesASession(t *testing.T) {
	app := buildApp()
	stop, ok := app.GetCommand("keyholder-stop")
	if !ok || stop.Hidden {
		t.Fatalf("keyholder-stop registered=%v hidden=%v, want a visible command", ok, ok && stop.Hidden)
	}
	hasSession := false
	for _, p := range stop.Params {
		if p.Name == "session" {
			hasSession = true
		}
	}
	if !hasSession {
		t.Error("keyholder-stop has no session parameter")
	}
	if resume, ok := app.GetCommand("resume"); !ok || resume.Hidden {
		t.Errorf("resume registered=%v, want a visible command", ok)
	}
}

// Every `sc session-key` refusal that asks the operator to act names a
// command that exists and is visible (principle 6), never `keyholder-pair`.
func TestSessionKeyRefusalsNameCommandsTheOperatorCanRun(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, e *sessionKeyEnv)
		target string
		want   []string
	}{
		{"no pair", func(*testing.T, *sessionKeyEnv) {}, "none", []string{"`sc resume repo/none`"}},
		{
			"dead pair", func(t *testing.T, e *sessionKeyEnv) { e.makeSessionDir(t, "down"); e.live = false }, "down",
			[]string{"`sc keyholder-stop repo/down`", "`sc resume repo/down`"},
		},
		{
			"no recorded fingerprint", func(t *testing.T, e *sessionKeyEnv) { e.makeSessionDir(t, "a"); e.pair.Fingerprint = "" }, "a",
			[]string{"`sc keyholder-stop repo/a`", "`sc resume repo/a`"},
		},
		{
			"changed key", func(t *testing.T, e *sessionKeyEnv) { e.makeSessionDir(t, "a"); e.pair.Fingerprint = "SHA256:RECORDED" }, "a",
			[]string{"`sc keyholder-stop repo/a`", "`sc resume repo/a`"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := useSessionKeySeams(t)
			c.setup(t, e)
			err := runSessionKey(context.Background(), json.RawMessage(`{"session":"`+c.target+`"}`))
			if err == nil {
				t.Fatal("want a refusal")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("refusal %q does not name %s", err, w)
				}
			}
			if strings.Contains(err.Error(), "keyholder-pair") || strings.Contains(err.Error(), "start it again") {
				t.Errorf("refusal %q still points at a hidden command or the old vague advice", err)
			}
		})
	}
}
