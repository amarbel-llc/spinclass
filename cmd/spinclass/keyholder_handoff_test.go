package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// handoffCapture is what the start hand-off was run with.
type handoffCapture struct {
	calls int
	dir   string
	env   []string
}

func stubStartHandoffExec(t *testing.T, err error) *handoffCapture {
	t.Helper()
	c := &handoffCapture{}
	prev := startHandoffExecFn
	startHandoffExecFn = func(dir string, _ []string, env []string) (string, error) {
		c.calls++
		c.dir, c.env = dir, env
		return "", err
	}
	t.Cleanup(func() { startHandoffExecFn = prev })
	return c
}

func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// A-M1: with the key holder on, the start hand-off no longer runs with the
// CALLER's environment: the child gets its own holder, the operator's agent
// variables are emptied, and the caller's own unit scope is gone.
func TestStartHandoffWithTheKeyHolderOnGivesTheChildItsOwnHolderAndNotTheCallersEnvironment(t *testing.T) {
	stoppedChildFixture(t, "holder", "observe,start", true)
	t.Setenv("SSH_AUTH_SOCK", "/run/user/1000/callers-agent.sock")
	t.Setenv("PIGGY_AUTH_SOCK", "/run/user/1000/piggy-forwarded.sock")
	t.Setenv("CLOWN_AGENT_SCOPE", "/callers/own/spec.json")
	holder := shortSocket(t)
	launches := stubKeyholderLaunch(t, map[string]string{"SSH_AUTH_SOCK": holder}, nil, nil)
	hand := stubStartHandoffExec(t, nil)

	if text, isErr := callStartChild(t, `{"child":"worker/kid","message":"carry on"}`); isErr {
		t.Fatalf("start: %s", text)
	}
	if hand.calls != 1 || hand.env == nil {
		t.Fatalf("hand-off ran %d time(s) with env %v, want one run with an explicit environment", hand.calls, hand.env)
	}
	if len(*launches) != 1 || !(*launches)[0].spawned || (*launches)[0].session != "worker/kid" {
		t.Errorf("the key holder was asked %+v, want one worker launch of worker/kid", *launches)
	}
	if v, _ := envValue(hand.env, "SSH_AUTH_SOCK"); v != holder {
		t.Errorf("SSH_AUTH_SOCK = %q, want the child's holder %q", v, holder)
	}
	if v, ok := envValue(hand.env, "PIGGY_AUTH_SOCK"); !ok || v != "" {
		t.Errorf("PIGGY_AUTH_SOCK = %q (present %v), want it emptied", v, ok)
	}
	if v, ok := envValue(hand.env, "CLOWN_AGENT_SCOPE"); ok {
		t.Errorf("the caller's CLOWN_AGENT_SCOPE reached an unconfined child: %q", v)
	}
}

// A confined child gets ITS OWN spec as the scope, brought in line with the
// switch, never the caller's, and never the operator's agent bound.
func TestStartHandoffOfAConfinedChildHandsItsOwnRetargetedSpec(t *testing.T) {
	_, wt := stoppedChildFixture(t, "holder", "observe,start", true)
	stateDir := os.Getenv("XDG_STATE_HOME")
	rec := confinement.UnitRecordPath(stateDir, wt)
	if err := os.MkdirAll(filepath.Dir(rec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte(`{"principal":"p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	specPath := confinement.UnitSpecPath(stateDir, wt)
	const stored = `{"version":1,"worktree":"/w","operator_home":"/h","result_path":"/r","binds_rw":[],"binds_ro":[],"deny_paths":[],"sockets":[{"path":"/run/user/1000/piggy-agent.sock","purpose":"piggy-agent"}],"env":{}}`
	if err := os.WriteFile(specPath, []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	// A confined child can be started through the hand-off only when a frontend
	// principal mapping names it (ownerPrincipal); a confined spawn leaves one.
	writeFrontendMapping(t, "kid-principal", "worker/kid", wt)
	t.Setenv("CLOWN_AGENT_SCOPE", "/callers/own/spec.json")
	holder := shortSocket(t)
	stubKeyholderLaunch(t, map[string]string{"SSH_AUTH_SOCK": holder}, nil, nil)
	hand := stubStartHandoffExec(t, nil)

	if text, isErr := callStartChild(t, `{"child":"worker/kid","message":"carry on"}`); isErr {
		t.Fatalf("start: %s", text)
	}
	if v, _ := envValue(hand.env, "CLOWN_AGENT_SCOPE"); v != specPath {
		t.Errorf("CLOWN_AGENT_SCOPE = %q, want the child's own spec %q", v, specPath)
	}
	v, raw := readSpecView(t, specPath)
	if len(v.Sockets) != 1 || v.Sockets[0].Path != holder || strings.Contains(raw, "piggy-agent") {
		t.Errorf("the child's stored spec still binds the operator's agent or not the holder: %s", raw)
	}
}

// The key holder off: the hand-off runs exactly as it always has, inheriting the
// environment (a nil env), and starts nothing.
func TestStartHandoffWithTheKeyHolderOffInheritsTheEnvironmentAsBefore(t *testing.T) {
	stoppedChildFixture(t, "holder", "observe,start", true)
	stubKeyholderLaunch(t, nil, nil, nil)
	hand := stubStartHandoffExec(t, nil)

	if text, isErr := callStartChild(t, `{"child":"worker/kid","message":"carry on"}`); isErr {
		t.Fatalf("start: %s", text)
	}
	if hand.calls != 1 || hand.env != nil {
		t.Errorf("hand-off ran %d time(s) with env %v, want one run inheriting the environment (nil)", hand.calls, hand.env)
	}
}

// A hand-off that fails leaves the session stopped and stops a pair this start
// launched, so nothing keeps a key the session never uses.
func TestStartHandoffFailureStopsThePairThisStartLaunched(t *testing.T) {
	repoPath, _ := stoppedChildFixture(t, "holder", "observe,start", true)
	undone := false
	stubKeyholderLaunch(t, map[string]string{"SSH_AUTH_SOCK": shortSocket(t)}, func() { undone = true }, nil)
	stubStartHandoffExec(t, errors.New("exit status 1"))

	if _, isErr := callStartChild(t, `{"child":"worker/kid","message":"carry on"}`); !isErr {
		t.Fatal("a failing hand-off was reported as a success")
	}
	if !undone {
		t.Error("the pair this start launched was left running")
	}
	if readKid(t, repoPath).StoppedAt == nil {
		t.Error("the session was marked started")
	}
}
