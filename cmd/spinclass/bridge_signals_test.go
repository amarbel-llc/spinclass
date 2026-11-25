package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/dewey/pkgs/mesa"

	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// signalEnv is a serve's environment for a session staged in the state home it
// returns: principal and key as spinclass and clown export them.
func signalEnv(t *testing.T) (stateHome string) {
	t.Helper()
	stateHome = t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", e2ePrincipal)
	t.Setenv("SPINCLASS_SESSION_ID", sessionTestKey)
	return stateHome
}

// freshWakes gives the test its own wake memory and clock.
func freshWakes(t *testing.T, now *time.Time) {
	t.Helper()
	origWakes, origNow := bridgeWakes, bridgeNow
	bridgeWakes = newWakeLimiter()
	bridgeNow = func() time.Time { return *now }
	t.Cleanup(func() { bridgeWakes, bridgeNow = origWakes, origNow })
}

func TestAFailedBridgeStartIsADeadListenerMarkedDownAndWokenOncePerInterval(t *testing.T) {
	stateHome := signalEnv(t)
	stageSessionKey(t, sessionTestKey, e2ePrincipal)
	calls := stubNotifyPrincipal(t)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	freshWakes(t, &now)
	dir := shortBridgeDir(t)
	orig := bridgeResolve
	bridgeResolve = func() bridgePlan { return bridgePlan{Dir: dir, Unit: e2eUnit, CgroupFile: "/x/y"} }
	t.Cleanup(func() { bridgeResolve = orig })
	// A start that fails: no peer check wired.
	origCheck := bridgeCheckPeer
	bridgeCheckPeer = nil
	t.Cleanup(func() { bridgeCheckPeer = origCheck })

	if stop := startBridge(context.Background(), e2eHandlerFactory()); stop != nil {
		stop()
		t.Fatal("the bridge started with no exact peer check")
	}
	if len(*calls) != 1 || (*calls)[0].target != "spawner" || !strings.Contains((*calls)[0].message, "peercheck.CheckPeerExact") {
		t.Fatalf("wakes = %+v, want one to the other holder naming the cause", *calls)
	}
	st, ok := crossinglog.ReadState(crossinglog.StateDir(stateHome), e2ePrincipal)
	if !ok || st.Listening || !strings.Contains(st.Reason, "peercheck.CheckPeerExact") || st.Session != sessionTestKey {
		t.Errorf("state file = %+v %v, want down with the named reason", st, ok)
	}
	if bridgeStateOut.Load() != nil {
		t.Error("the state writer outlived the failed start")
	}

	// A second failed start inside the interval is counted and marked but does
	// not page again; after the interval it does.
	if stop := startBridge(context.Background(), e2eHandlerFactory()); stop != nil {
		stop()
	}
	if len(*calls) != 1 {
		t.Errorf("a second wake inside the interval: %+v", *calls)
	}
	now = now.Add(bridgeWakeInterval + time.Minute)
	if stop := startBridge(context.Background(), e2eHandlerFactory()); stop != nil {
		stop()
	}
	if len(*calls) != 2 {
		t.Errorf("no wake after the interval: %+v", *calls)
	}
}

func TestAListeningBridgeWritesItsStateAndRemovesItOnStop(t *testing.T) {
	stateHome := signalEnv(t)
	stageSessionKey(t, sessionTestKey, e2ePrincipal)
	cg := wireTestPeerCheck(t, nil)
	dir := shortBridgeDir(t)
	orig := bridgeResolve
	bridgeResolve = func() bridgePlan { return bridgePlan{Dir: dir, Unit: e2eUnit, CgroupFile: cg} }
	t.Cleanup(func() { bridgeResolve = orig })

	stop := startBridge(context.Background(), e2eHandlerFactory())
	if stop == nil {
		t.Fatal("the bridge did not start")
	}
	st, ok := crossinglog.ReadState(crossinglog.StateDir(stateHome), e2ePrincipal)
	if !ok || !st.Listening || st.Reason != "" {
		t.Errorf("state while listening = %+v %v", st, ok)
	}
	stop()
	if _, ok := crossinglog.ReadState(crossinglog.StateDir(stateHome), e2ePrincipal); ok {
		t.Error("the state file of a stopped serve was left behind")
	}
	if bridgeStateOut.Load() != nil {
		t.Error("the state writer outlived stop")
	}
}

func TestAHolderlessSessionWakesItsSpawner(t *testing.T) {
	signalEnv(t)
	stageSession(t, "bridge-test", "session-key", e2ePrincipal, nil)
	calls := stubNotifyPrincipal(t)
	now := time.Now()
	freshWakes(t, &now)
	bridgeWake(sessionTestKey, "listener", "died", "the bridge died")
	if len(*calls) != 1 || (*calls)[0].target != "spawner" {
		t.Errorf("wakes = %+v, want one to the record's spawner", *calls)
	}
}

func TestTheObserverSignalsTheFirstVersionErrorAndFiveRefusalsInARow(t *testing.T) {
	var got []string
	obs := &bridgeObserver{signal: func(_, kind, msg string) {
		if msg == "" {
			t.Errorf("a %s signal with no text", kind)
		}
		got = append(got, kind)
	}}
	caller := bridgeserver.Caller{SessionKey: "repo/branch"}
	ev := func(service, outcome string) bridgeserver.Event {
		return bridgeserver.Event{Service: service, Outcome: outcome, Caller: caller}
	}
	send := func(n int, service, outcome string) {
		for i := 0; i < n; i++ {
			obs.Crossing(ev(service, outcome))
		}
	}

	send(1, "session", "version")
	send(3, "session", "version") // only the first says anything
	if strings.Join(got, ",") != "version" {
		t.Fatalf("after four version errors: %v, want exactly one version signal", got)
	}

	got = nil
	send(4, "", "refused")
	send(1, "control", "ok") // a served stream ends the run
	send(4, "", "refused")
	send(3, "egress", "refused") // an unknown service is not a peer refusal
	if len(got) != 0 {
		t.Fatalf("signals %v before five refusals in a row", got)
	}
	send(1, "", "refused")
	if strings.Join(got, ",") != "peer-refused" {
		t.Fatalf("after five refusals in a row: %v, want one peer-refused", got)
	}
	send(4, "", "refused")
	if len(got) != 1 {
		t.Errorf("the run was not reset after a signal: %v", got)
	}
}

func TestAnObserverWithNoSignalSendsNone(t *testing.T) {
	obs := &bridgeObserver{}
	for i := 0; i < 10; i++ {
		obs.Crossing(bridgeserver.Event{Outcome: "refused"})
		obs.Crossing(bridgeserver.Event{Service: "session", Outcome: "version"})
	}
}

func TestSessionStreamOpenedIsCounted(t *testing.T) {
	drain := queueStatsdSink(t)
	p := bridgeSessionProvider{stats: statsd.Resolve("", false)}
	p.emitStreamOpened()
	got := metricDatagrams(t, drain, 1)
	if len(got) != 1 || got[0] != "spinclass.bridge.session.stream.opened:1|c" {
		t.Errorf("datagrams = %v", got)
	}
}

func TestVersionIsItsOwnOutcomeInTheMetrics(t *testing.T) {
	drain := queueStatsdSink(t)
	obs := &bridgeObserver{stats: statsd.Resolve("", false)}
	obs.Crossing(bridgeserver.Event{Service: "control", Outcome: "version", Caller: bridgeserver.Caller{SessionKey: "repo/branch"}})
	got := metricDatagrams(t, drain, 2)
	found := false
	for _, g := range got {
		if g == "spinclass.bridge.control.version:1|c" {
			found = true
		}
	}
	if !found {
		t.Errorf("datagrams = %v, want spinclass.bridge.control.version", got)
	}
}

func TestSessionHealthAndListShowABridgeThatIsDown(t *testing.T) {
	stateHome := signalEnv(t)
	dir := crossinglog.StateDir(stateHome)
	if err := crossinglog.WriteState(dir, "p-1", crossinglog.State{Session: "repo/b", Listening: false, Reason: "the unit clown named is not the recorded one", Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := crossinglog.WriteState(dir, "p-2", crossinglog.State{Session: "repo/c", Listening: true, Since: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if got := bridgeStateNote(stateHome, "p-1", true); got != "bridge DOWN: the unit clown named is not the recorded one" {
		t.Errorf("down note = %q", got)
	}
	if got := bridgeStateNote(stateHome, "p-2", true); got != "bridge listening" {
		t.Errorf("session-health note for a live bridge = %q", got)
	}
	if got := bridgeStateNote(stateHome, "p-2", false); got != "" {
		t.Errorf("sc list says %q about a live bridge; it speaks only of a down one", got)
	}
	if got := bridgeStateNote(stateHome, "no-file", true); got != "" {
		t.Errorf("a session with no state file got %q", got)
	}

	// session-health: the line of a held session carries the note.
	stageSession(t, "repo", "b", "p-1", []string{"caller-x"})
	d := fakeHealthDeps(nil, 0, false)
	d.Bridge = func(st *session.State) string { return bridgeStateNote(stateHome, st.Principal, true) }
	out, err := runSessionHealthWith(sessionHealthParams{}, "caller-x", d)
	if err != nil || !strings.Contains(out, "bridge DOWN: the unit clown named is not the recorded one") {
		t.Errorf("session-health = %q, %v", out, err)
	}

	// sc list: plain and TTY both name it.
	state := func() []session.State {
		return []session.State{{
			SessionState: session.StateInactive, WorktreePath: t.TempDir(), Branch: "b",
			SessionKey: "repo/b", Principal: "p-1", StartedAt: time.Now(),
		}}
	}
	for name, width := range map[string]int{"plain": 0, "tty": 140} {
		opt := mesa.ForcePlain()
		if width > 0 {
			opt = mesa.ForceStyle()
		}
		list := renderListTable(state(), nil, nil, false, width, opt)
		if !strings.Contains(list, "bridge DOWN") {
			t.Errorf("%s list does not show the down bridge:\n%s", name, list)
		}
	}
	if list := renderListTable([]session.State{{
		SessionState: session.StateInactive, WorktreePath: t.TempDir(), Branch: "c",
		SessionKey: "repo/c", Principal: "p-2", StartedAt: time.Now(),
	}}, nil, nil, false, 0, mesa.ForcePlain()); strings.Contains(list, "bridge") {
		t.Errorf("a live bridge is noise in sc list:\n%s", list)
	}
}

func TestAServeClownGaveNoBridgeReadsNothingAndSaysNothing(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// A directory that is no git checkout: if the plan read the checkout first,
	// its reason would be about git, not about the environment.
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	for name, env := range map[string][2]string{"unset": {}, "present and empty": {"", " "}} {
		t.Run(name, func(t *testing.T) {
			if name != "unset" {
				t.Setenv("CLOWN_BRIDGE_DIR", env[0])
				t.Setenv("CLOWN_BRIDGE_UNIT", env[1])
				t.Setenv("CLOWN_BRIDGE_CGROUP_FILE", "")
			} else {
				for _, k := range []string{"CLOWN_BRIDGE_DIR", "CLOWN_BRIDGE_UNIT", "CLOWN_BRIDGE_CGROUP_FILE"} {
					t.Setenv(k, "")
					_ = os.Unsetenv(k)
				}
			}
			p := bridgeResolve()
			if p.Dir != "" || !p.Quiet || p.Reason != bridgeEnvUnsetReason {
				t.Errorf("plan = %+v, want a quiet plan whose reason is the environment's", p)
			}
		})
	}
}

func TestTheBridgeNeverMakesAnImplicitSession(t *testing.T) {
	stateHome := signalEnv(t)
	t.Setenv("SPINCLASS_SESSION_ID", "")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	cg := wireTestPeerCheck(t, nil)
	dir := shortBridgeDir(t)
	orig := bridgeResolve
	bridgeResolve = func() bridgePlan { return bridgePlan{Dir: dir, Unit: e2eUnit, CgroupFile: cg} }
	t.Cleanup(func() { bridgeResolve = orig })

	if stop := startBridge(context.Background(), e2eHandlerFactory()); stop != nil {
		stop()
		t.Fatal("the bridge started with no SPINCLASS_SESSION_ID")
	}
	lazyImplicit.Lock()
	key := lazyImplicit.key
	lazyImplicit.Unlock()
	if key != "" {
		t.Errorf("an implicit session %q was materialised", key)
	}
	if entries, _ := os.ReadDir(filepath.Join(stateHome, "spinclass", "records")); len(entries) != 0 {
		t.Errorf("session records were written: %v", entries)
	}
}
