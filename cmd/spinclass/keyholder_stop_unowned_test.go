package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// khAccountUnits is the fake runner that can also say which units exist, as
// systemd does for the whole account whatever state root the caller runs under.
type khAccountUnits struct{ *khFakeRunner }

func (l khAccountUnits) ListUnits(context.Context) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for u := range l.active {
		out = append(out, u)
	}
	sort.Strings(out)
	return out, nil
}

// foreignPair starts a pair under ANOTHER state root on the same fake runner (the
// units are shared, the directories are not) and points the sweep's launcher at
// an empty state root of its own.
func foreignPair(t *testing.T, fx stopFixture, session string) {
	t.Helper()
	base, err := os.MkdirTemp("", "khf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	other := &keyholder.Launcher{Base: filepath.Join(base, "kh"), Tools: keyholder.Tools{Fibby: "/pin/fibby", Piggy: "/pin/piggy"}, Run: fx.f, Wait: time.Second}
	if _, err := other.Start(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	fx.l.Run = khAccountUnits{fx.f}
}

func agentActive(fx stopFixture, session string) bool {
	return fx.f.active["spinclass-keyholder-agent-"+keyholder.Hash(session)+".service"]
}

// A shell whose XDG_STATE_HOME is something else sees every live pair of the
// account as units with no directory. `sc keyholder-stop --all` must not stop
// them: they are not this state root's.
func TestKeyholderStopAllLeavesAnotherStateRootsPairsRunning(t *testing.T) {
	fx := newStopFixture(t, "s/mine")
	foreignPair(t, fx, "s/foreign")
	_, stderr := stubOperatorTerminal(t, true)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{All: true}); err != nil {
		t.Fatal(err)
	}
	if agentActive(fx, "s/mine") {
		t.Error("this state root's own pair was not stopped")
	}
	if !agentActive(fx, "s/foreign") {
		t.Fatal("a pair another state root recorded was stopped by this one")
	}
	if !strings.Contains(stderr.String(), "not recorded under this state root") || !strings.Contains(stderr.String(), "--including-unowned") {
		t.Errorf("stderr does not say the unowned pair was left, or how to stop it:\n%s", stderr.String())
	}
}

// The dry run lists them as NOT MINE and stops nothing.
func TestKeyholderStopDryRunListsAnotherStateRootsPairsAsNotMine(t *testing.T) {
	fx := newStopFixture(t, "s/mine")
	foreignPair(t, fx, "s/foreign")
	stdout, _ := stubOperatorTerminal(t, false)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{All: true, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "NOT MINE "+keyholder.Hash("s/foreign")) {
		t.Errorf("the foreign pair is not listed as NOT MINE:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "NOT MINE "+keyholder.Hash("s/mine")) {
		t.Errorf("this state root's own pair is listed as NOT MINE:\n%s", stdout.String())
	}
	if !agentActive(fx, "s/mine") || !agentActive(fx, "s/foreign") {
		t.Error("--dry-run stopped a pair")
	}
}

// The operator's explicit flag stops them too, says so first, and journals them
// apart from the ones this root owns.
func TestKeyholderStopIncludingUnownedStopsThemAndJournalsThemApart(t *testing.T) {
	fx := newStopFixture(t, "s/mine")
	foreignPair(t, fx, "s/foreign")
	stdout, _ := stubOperatorTerminal(t, true)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{All: true, IncludeUnowned: true}); err != nil {
		t.Fatal(err)
	}
	if agentActive(fx, "s/foreign") || agentActive(fx, "s/mine") {
		t.Error("--including-unowned left a pair running")
	}
	if !strings.Contains(stdout.String(), "will also stop 1 pair(s) this state root did not record") {
		t.Errorf("the flag did not say what it will also stop:\n%s", stdout.String())
	}
	b, err := os.ReadFile(fx.journal)
	if err != nil || !strings.Contains(string(b), `"action":"stop-unowned"`) {
		t.Errorf("the unowned stop was not journalled apart: %v\n%s", err, b)
	}
}
