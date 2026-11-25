package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"code.linenisgreat.com/clown/pkgs/peercheck"
	"code.linenisgreat.com/spinclass/internal/0/bridge"
	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
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

// Production wiring: the build's peer check IS clown's strict
// peercheck.CheckPeerExact, not a stand-in. With the env and the trusted
// [bridge] enable both set the listener starts; with either missing it stays
// off.
func TestProductionWiringIsClownsExactPeerCheck(t *testing.T) {
	if bridgeCheckPeer == nil {
		t.Fatal("no exact peer check is wired: the bridge can never start")
	}
	if got, want := reflect.ValueOf(bridgeCheckPeer).Pointer(), reflect.ValueOf(peercheck.CheckPeerExact).Pointer(); got != want {
		t.Fatalf("bridgeCheckPeer is not peercheck.CheckPeerExact")
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
	if stop == nil {
		t.Fatalf("env, trusted enable and the real exact check are set, yet the bridge is off: %v", *failures)
	}
	defer stop()
	if _, err := os.Lstat(filepath.Join(dir, "bridge.sock")); err != nil {
		t.Errorf("the listener left no socket: %v", err)
	}
	if len(*failures) != 0 {
		t.Errorf("failures = %v, want none", *failures)
	}

	// Either switch missing keeps it off: no env names no bridge, no trusted
	// enable names the reason.
	if p := resolveBridgePlan(bridgeTestEnv("", ""), home, repo); p.Dir != "" || !p.Quiet {
		t.Errorf("no env must mean no bridge: %+v", p)
	}
	noEnable := t.TempDir()
	if p := resolveBridgePlan(getenv, noEnable, filepath.Join(noEnable, "work", "repo")); p.Dir != "" || p.Reason == "" {
		t.Errorf("no trusted enable must mean no bridge with a reason: %+v", p)
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

// The start-order tests (the principal mapping and the unit record are
// readiness of the session service, not conditions of starting) live in
// bridge_start_order_test.go.

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
