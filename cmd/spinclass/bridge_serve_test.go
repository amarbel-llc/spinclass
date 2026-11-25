package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

func bridgeTestEnv(dir, unit string) func(string) string {
	env := map[string]string{}
	if dir != "" {
		env[bridge.EnvDir] = dir
	}
	if unit != "" {
		env[bridge.EnvUnit] = unit
	}
	return func(k string) string { return env[k] }
}

// wireTestPeerCheck wires check (nil admits) as the build's exact peer check
// for the test and returns a valid 0600 cgroup file for the plan, outside any
// bridge directory.
func wireTestPeerCheck(t *testing.T, check func(net.Conn, string) error) string {
	t.Helper()
	if check == nil {
		check = func(net.Conn, string) error { return nil }
	}
	old := bridgeCheckPeer
	bridgeCheckPeer = check
	t.Cleanup(func() { bridgeCheckPeer = old })
	p := filepath.Join(t.TempDir(), "bridge.cgroup")
	if err := os.WriteFile(p, []byte("/clown-agent.slice/"+e2eUnit), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// withCgroupFile adds the cgroup file to an environment.
func withCgroupFile(base func(string) string, file string) func(string) string {
	return func(k string) string {
		if k == bridge.EnvCgroupFile {
			return file
		}
		return base(k)
	}
}

// Production wiring: with the env and the trusted [bridge] enable both set, a
// build that has no exact peer check wired stays off, with the named reason,
// and leaves nothing in the bridge directory. When a clown that has
// CheckPeerExact is wired this test is replaced by one that it listens.
func TestProductionWiringStaysOffUntilAnExactPeerCheckIsWired(t *testing.T) {
	if bridgeCheckPeer != nil {
		t.Fatal("this build wires an exact peer check: replace this test with the one that pins that wiring")
	}
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBridgeSweatfile(t, filepath.Join(home, ".config", "spinclass", "sweatfile"), "[bridge]\nenable = true\n")
	dir := shortBridgeDir(t)
	cg := filepath.Join(t.TempDir(), "bridge.cgroup")
	if err := os.WriteFile(cg, []byte("/clown-agent.slice/"+e2eUnit), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := withCgroupFile(bridgeTestEnv(dir, e2eUnit), cg)
	if p := resolveBridgePlan(getenv, home, repo); p.Dir != dir || p.CgroupFile != cg {
		t.Fatalf("env and enable must make a plan: %+v", p)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("CLOWN_SESSION_ID", e2ePrincipal)
	t.Setenv("SPINCLASS_SESSION_ID", sessionTestKey)
	stageSessionKey(t, sessionTestKey, e2ePrincipal)
	failures := stubBridgeFailureHook(t)
	old := bridgeResolve
	bridgeResolve = func() bridgePlan { return resolveBridgePlan(getenv, home, repo) }
	t.Cleanup(func() { bridgeResolve = old })

	stop := startBridge(context.Background(), e2eHandlerFactory())
	if stop != nil {
		stop()
		t.Fatal("the bridge listens with no exact peer check wired")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the bridge directory is not empty: %v", entries)
	}
	if len(*failures) != 1 || !strings.Contains((*failures)[0], "peercheck.CheckPeerExact") {
		t.Errorf("failures = %v, want one naming peercheck.CheckPeerExact", *failures)
	}
}

func writeBridgeSweatfile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveBridgePlanGates(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".config", "spinclass", "sweatfile")
	env := bridgeTestEnv("/run/bridge", "agent.service")

	// Without the trusted switch the listener is off, env or not.
	if p := resolveBridgePlan(env, home, repo); p.Dir != "" || p.Reason == "" {
		t.Errorf("no [bridge] enable: %+v, want off with a reason", p)
	}

	// A repo-layer sweatfile with [bridge] enable = true does NOT turn it on.
	writeBridgeSweatfile(t, filepath.Join(repo, "sweatfile"), "[bridge]\nenable = true\n")
	if p := resolveBridgePlan(env, home, repo); p.Dir != "" {
		t.Errorf("repo-layer enable turned the bridge on: %+v", p)
	}

	// A trusted layer with the switch, but no env: still off.
	writeBridgeSweatfile(t, global, "[bridge]\nenable = true\n")
	for name, getenv := range map[string]func(string) string{
		"no env":  bridgeTestEnv("", ""),
		"no dir":  bridgeTestEnv("", "agent.service"),
		"no unit": bridgeTestEnv("/run/bridge", ""),
	} {
		if p := resolveBridgePlan(getenv, home, repo); p.Dir != "" {
			t.Errorf("%s: bridge on: %+v", name, p)
		}
	}

	// Both: on, with the env's dir and unit.
	p := resolveBridgePlan(env, home, repo)
	if p.Dir != "/run/bridge" || p.Unit != "agent.service" {
		t.Errorf("trusted enable with env: %+v", p)
	}
}

func shortBridgeDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "bs")
	if err != nil {
		d = t.TempDir()
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func TestStartBridgeFromListensAndStopRemovesTheSocket(t *testing.T) {
	dir := shortBridgeDir(t)
	allow := func(net.Conn, string) error { return nil }
	stop := startBridgeFrom(context.Background(), bridgePlan{Dir: dir, Unit: "agent.service"},
		bridgeserver.Options{SessionKey: "repo/branch", PeerCheck: allow})
	if stop == nil {
		t.Fatal("listener did not start")
	}
	sock := filepath.Join(dir, bridge.SocketName)
	c, err := net.Dial("unix", sock)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("BRIDGE/1 control\n")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	b := make([]byte, 3)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "OK\n" {
		t.Errorf("control hello: %q %v", b, err)
	}
	_ = c.Close()
	stop()
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Errorf("socket remains after stop: %v", err)
	}
}

// serve listens for a session only when the frontend's own principal-to-session
// resolution agrees with the key the listener will carry: the key every bridged
// call then acts on. Anything else stays off, once, counted like a failed start.
func TestStartBridgeStaysOffUnlessThePrincipalResolvesToTheListenersKey(t *testing.T) {
	for _, tc := range []struct {
		name          string
		principalOf   func(f liarFixture) string // CLOWN_SESSION_ID of the serve
		wantListening bool
		wantErr       string
	}{
		{"the principal resolves to the listener's session", func(f liarFixture) string { return f.a.principal }, true, ""},
		{"the principal resolves to another session", func(f liarFixture) string { return f.b.principal }, false, "resolves to " + "beta-repo/b-branch"},
		{"the principal resolves to nothing", func(liarFixture) string { return "not-stamped-yet" }, false, "resolves to no session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLiarFixture(t)
			// The listener's key is session A's; only the principal differs.
			t.Setenv("SPINCLASS_SESSION_ID", f.a.key)
			t.Setenv("CLOWN_SESSION_ID", tc.principalOf(f))
			// A's mapping told the truth before newLiarFixture lied; the principal
			// of the control case must resolve through a true mapping.
			if err := confinement.RecordUnconfinedPrincipal(os.Getenv("XDG_STATE_HOME"), f.a.principal, f.a.key, f.a.wt); err != nil {
				t.Fatal(err)
			}
			failures := stubBridgeFailureHook(t)
			dir := shortBridgeDir(t)
			cg := wireTestPeerCheck(t, nil)
			old := bridgeResolve
			bridgeResolve = func() bridgePlan { return bridgePlan{Dir: dir, Unit: e2eUnit, CgroupFile: cg} }
			t.Cleanup(func() { bridgeResolve = old })

			stop := startBridge(context.Background(), e2eHandlerFactory())
			if stop != nil {
				defer stop()
			}
			_, serr := os.Lstat(filepath.Join(dir, bridge.SocketName))
			if tc.wantListening {
				if stop == nil || serr != nil || len(*failures) != 0 {
					t.Fatalf("stop=%v socket=%v failures=%v, want a listener and no failure", stop != nil, serr, *failures)
				}
				return
			}
			if stop != nil || !os.IsNotExist(serr) {
				t.Errorf("the bridge listens (stop=%v, socket %v) though the principal does not resolve to the listener's key", stop != nil, serr)
			}
			if len(*failures) != 1 || !strings.HasPrefix((*failures)[0], f.a.key+"|") || !strings.Contains((*failures)[0], tc.wantErr) {
				t.Errorf("failures = %v, want exactly one for %s naming %q", *failures, f.a.key, tc.wantErr)
			}
		})
	}
}

// The unit clown names must be the unit the frontend recorded for the session.
// No readable record is not a mismatch; a record naming another unit is, and
// counts like a failed start.
func TestStartBridgeStaysOffWhenTheRecordedUnitIsAnother(t *testing.T) {
	for _, tc := range []struct {
		name      string
		recorded  string
		hasRecord bool
		listening bool
	}{
		{"the record names the same unit", e2eUnit, true, true},
		{"the record names another unit", "clown-agent-someone-else.service", true, false},
		{"no readable record", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CLOWN_SESSION_ID", e2ePrincipal)
			t.Setenv("SPINCLASS_SESSION_ID", sessionTestKey)
			stageSessionKey(t, sessionTestKey, e2ePrincipal)
			failures := stubBridgeFailureHook(t)
			cg := wireTestPeerCheck(t, nil)
			origUnit := bridgeRecordedUnit
			bridgeRecordedUnit = func(string) (string, bool) { return tc.recorded, tc.hasRecord }
			t.Cleanup(func() { bridgeRecordedUnit = origUnit })
			dir := shortBridgeDir(t)
			old := bridgeResolve
			bridgeResolve = func() bridgePlan { return bridgePlan{Dir: dir, Unit: e2eUnit, CgroupFile: cg} }
			t.Cleanup(func() { bridgeResolve = old })

			stop := startBridge(context.Background(), e2eHandlerFactory())
			if stop != nil {
				defer stop()
			}
			if tc.listening {
				if stop == nil || len(*failures) != 0 {
					t.Fatalf("stop=%v failures=%v, want a listener and no failure", stop != nil, *failures)
				}
				return
			}
			if stop != nil {
				t.Error("the bridge listens for a unit the record does not name")
			}
			if _, err := os.Lstat(filepath.Join(dir, bridge.SocketName)); !os.IsNotExist(err) {
				t.Errorf("a socket was bound: %v", err)
			}
			if len(*failures) != 1 || !strings.Contains((*failures)[0], tc.recorded) || !strings.Contains((*failures)[0], e2eUnit) {
				t.Errorf("failures = %v, want one naming both units", *failures)
			}
		})
	}
}

// Any failure to start is a nil stop and serve goes on.
func TestStartBridgeFromNeverFailsServe(t *testing.T) {
	loose := shortBridgeDir(t)
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, plan := range map[string]bridgePlan{
		"off plan":     {Reason: "not enabled"},
		"loose dir":    {Dir: loose, Unit: "agent.service"},
		"missing dir":  {Dir: filepath.Join(loose, "missing"), Unit: "agent.service"},
		"bad unit":     {Dir: shortBridgeDir(t), Unit: "agent.scope"},
		"relative dir": {Dir: "rel", Unit: "agent.service"},
	} {
		if stop := startBridgeFrom(context.Background(), plan, bridgeserver.Options{}); stop != nil {
			stop()
			t.Errorf("%s: listener started", name)
		}
	}
}
