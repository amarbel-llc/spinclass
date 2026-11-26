package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// khFakeRunner is a stand-in for systemd and the programs: starting the fibby
// unit creates its sockets, starting the agent unit creates its socket.
type khFakeRunner struct {
	t         *testing.T
	mu        sync.Mutex
	started   []string
	active    map[string]bool
	listeners []net.Listener
	mintErr   error
	stuck     map[string]bool // units that stay active after Stop
}

func newKhFake(t *testing.T) *khFakeRunner {
	f := &khFakeRunner{t: t, active: map[string]bool{}}
	t.Cleanup(func() {
		for _, l := range f.listeners {
			_ = l.Close()
		}
	})
	return f
}

func (f *khFakeRunner) listen(path string) {
	l, err := net.Listen("unix", path)
	if err != nil {
		f.t.Fatalf("fake socket: %v", err)
	}
	f.listeners = append(f.listeners, l)
}

func flagValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func (f *khFakeRunner) Start(_ context.Context, unit string, argv, _ []string, _ ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, unit)
	f.active[unit] = true
	if strings.Contains(unit, "fibby") {
		f.listen(flagValue(argv, "--socket"))
		f.listen(flagValue(argv, "--control-socket"))
	} else {
		f.listen(flagValue(argv, "-a"))
	}
	return nil
}

func (f *khFakeRunner) Active(_ context.Context, unit string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[unit], nil
}

func (f *khFakeRunner) Stop(_ context.Context, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.stuck[unit] {
		f.active[unit] = false
	}
	return nil
}

func (f *khFakeRunner) Output(_ context.Context, argv, _ []string) ([]byte, error) {
	if len(argv) >= 3 && argv[1] == "holder" && argv[2] == "mint" {
		if f.mintErr != nil {
			return nil, f.mintErr
		}
		return []byte("ssh-ed25519 AAAAC3NzaFAKEAAA holder:reader\n"), nil
	}
	return nil, nil
}

// khHome writes a global sweatfile with the given body and returns the home
// directory and a repo path under it.
func khHome(t *testing.T, body string) (home, repo string) {
	t.Helper()
	home, err := os.MkdirTemp("", "khh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	// Each test gets its own state root: a session's kind record (driver or
	// worker) is written there at its first launch with the switch on, and a
	// record left by another test for the same session key would decide this one.
	stateRoot := filepath.Join(home, "state", "kh")
	prevBase := keyholderStateBaseFn
	keyholderStateBaseFn = func() string { return stateRoot }
	t.Cleanup(func() { keyholderStateBaseFn = prevBase })
	repo = filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		p := filepath.Join(home, ".config", "spinclass", "sweatfile")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, repo
}

func khUseFake(t *testing.T, f *khFakeRunner) (launcherBuilt *int) {
	t.Helper()
	base, err := os.MkdirTemp("", "khb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	// A production build pins these; a launch bakes ssh-keygen into the
	// session's git signing program.
	t.Cleanup(keyholder.OverrideSSHPins("/pin/ssh-add", "/pin/ssh-keygen"))
	built := 0
	prev := keyholderLauncherFn
	t.Cleanup(func() { keyholderLauncherFn = prev })
	keyholderLauncherFn = func(keyholder.Tools) (*keyholder.Launcher, error) {
		built++
		return &keyholder.Launcher{Base: filepath.Join(base, "kh"), Tools: keyholder.Tools{Fibby: "/pin/fibby", Piggy: "/pin/piggy"}, Run: f, Wait: time.Second}, nil
	}
	return &built
}

func TestKeyholderOffStartsNothingAndLeavesTheEnvironmentAlone(t *testing.T) {
	home, repo := khHome(t, "") // no [keyholder] at all
	f := newKhFake(t)
	built := khUseFake(t, f)
	env, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{})
	if err != nil || env != nil {
		t.Fatalf("mode unset: got (%v, %v), want (nil, nil)", env, err)
	}
	if *built != 0 || len(f.started) != 0 {
		t.Errorf("an off switch built a launcher (%d) or started units (%v)", *built, f.started)
	}
	in := map[string]string{"SSH_AUTH_SOCK": "/operator/agent.sock"}
	if got := withKeyholderEnv(in, env); got["SSH_AUTH_SOCK"] != "/operator/agent.sock" {
		t.Errorf("an off switch changed the environment: %v", got)
	}
}

func TestKeyholderOnPointsTheSessionAtItsOwnHolderInsteadOfTheOperators(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	f := newKhFake(t)
	khUseFake(t, f)
	env, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	sock := env["SSH_AUTH_SOCK"]
	if !strings.HasSuffix(sock, filepath.Join(keyholder.Hash("repo/a"), "agent", "agent.sock")) {
		t.Fatalf("SSH_AUTH_SOCK = %q, want the session's holder socket", sock)
	}
	got := withKeyholderEnv(map[string]string{"SSH_AUTH_SOCK": "/operator/agent.sock", "OTHER": "kept"}, env)
	if got["SSH_AUTH_SOCK"] != sock || got["OTHER"] != "kept" {
		t.Errorf("the entry env was %v, want the holder replacing the operator's agent and the rest kept", got)
	}
	// A second launch of the same session reuses the pair.
	if _, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(f.started) != 2 {
		t.Errorf("units started = %v, want exactly the pair (2)", f.started)
	}
}

// A switch that is on but cannot be honoured refuses the launch: the session
// never quietly runs on the operator's agent.
func TestKeyholderOnThatCannotStartRefusesTheLaunch(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	f := newKhFake(t)
	f.mintErr = errors.New("holder refused")
	khUseFake(t, f)
	env, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "holder refused") || env != nil {
		t.Fatalf("got (%v, %v), want an error and no environment", env, err)
	}
}

func TestKeyholderInvalidModeRefusesTheLaunchNamingTheValue(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"yes\"\n")
	built := khUseFake(t, newKhFake(t))
	_, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `"yes"`) {
		t.Fatalf("got %v, want an error naming the value", err)
	}
	if *built != 0 {
		t.Error("an invalid mode built a launcher")
	}
}

func TestKeyholderInRepoLayerIsIgnoredAndReported(t *testing.T) {
	home, repo := khHome(t, "")
	if err := os.WriteFile(filepath.Join(repo, "sweatfile"), []byte("[keyholder]\nmode = \"on\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newKhFake(t)
	khUseFake(t, f)
	var stderr bytes.Buffer
	env, err := keyholderSessionEnv(home, repo, "repo/a", &stderr)
	if err != nil || env != nil || len(f.started) != 0 {
		t.Fatalf("a repo-layer on started the key holder: (%v, %v, %v)", env, err, f.started)
	}
	if !strings.Contains(stderr.String(), "only layers above the repo") {
		t.Errorf("the ignored layer was not reported: %q", stderr.String())
	}
}

// Switching it off after a session had a pair: a new session gets no pair, no
// units and no directory, and its environment is untouched; the old session's
// pair is the close path's to stop.
func TestKeyholderSwitchedOffLeavesNoPairForNewSessions(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	f := newKhFake(t)
	built := khUseFake(t, f)
	if _, err := keyholderSessionEnv(home, repo, "repo/old", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	before := len(f.started)
	if err := os.WriteFile(filepath.Join(home, ".config", "spinclass", "sweatfile"), []byte("[keyholder]\nmode = \"off\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := keyholderSessionEnv(home, repo, "repo/new", &bytes.Buffer{})
	if err != nil || env != nil {
		t.Fatalf("after switching off: (%v, %v)", env, err)
	}
	if len(f.started) != before {
		t.Errorf("a new session started units after the switch went off: %v", f.started[before:])
	}
	l, _ := keyholderLauncherFn(keyholder.Tools{})
	if _, serr := os.Lstat(keyholder.SessionDir(l.Base, "repo/new")); !os.IsNotExist(serr) {
		t.Errorf("a new session has a key holder directory after the switch went off: %v", serr)
	}
	if *built != 2 { // the first launch, and this test's own probe just above
		t.Errorf("launcher built %d times, want 2 (no launcher for the off session)", *built)
	}
}
