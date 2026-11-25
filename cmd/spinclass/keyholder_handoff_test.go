package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
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

// confinedKidFixture makes worker/kid a confined stopped child whose stored unit
// spec binds the given sockets, and returns the child's worktree and spec path. A
// confined child can be started through the hand-off only when a frontend
// principal mapping names it (ownerPrincipal); a confined spawn leaves one.
func confinedKidFixture(t *testing.T, sockets, env string) (wt, specPath string) {
	t.Helper()
	_, wt = stoppedChildFixture(t, "holder", "observe,start", true)
	stateDir := os.Getenv("XDG_STATE_HOME")
	rec := confinement.UnitRecordPath(stateDir, wt)
	if err := os.MkdirAll(filepath.Dir(rec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte(`{"principal":"p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	specPath = confinement.UnitSpecPath(stateDir, wt)
	// A holder spec as the builder writes it also binds the certificate directory
	// read-only and names the certificate file.
	bindsRO := `[]`
	if _, after, ok := strings.Cut(sockets, `"path":"`); ok && strings.Contains(sockets, `"key-holder"`) {
		holder, _, _ := strings.Cut(after, `"`)
		dir := keyholder.CertDirOfAgentSock(holder)
		bindsRO = `["` + dir + `"]`
		env = strings.Replace(env, `{`, `{"`+keyholder.CertEnv+`":"`+filepath.Join(dir, "cert.pub")+`",`, 1)
		env = strings.Replace(env, `,}`, `}`, 1)
	}
	stored := `{"version":1,"worktree":"/w","operator_home":"/h","result_path":"/r","binds_rw":[],"binds_ro":` + bindsRO + `,"deny_paths":[],"sockets":` + sockets + `,"env":` + env + `}`
	if err := os.WriteFile(specPath, []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFrontendMapping(t, "kid-principal", "worker/kid", wt)
	return wt, specPath
}

// A confined child whose stored spec matches the switch (a holder spec, switch
// on) gets ITS OWN spec as the scope, never the caller's, and the state
// directory clown finds its sidecar record under, and the spec is left untouched.
func TestStartHandoffOfAConfinedChildHandsItsOwnSpecAndStateDir(t *testing.T) {
	holder := shortSocket(t)
	_, specPath := confinedKidFixture(t, `[{"path":"`+holder+`","purpose":"key-holder"}]`, `{"SSH_AUTH_SOCK":"`+holder+`"}`)
	before, _ := os.ReadFile(specPath)
	t.Setenv("CLOWN_AGENT_SCOPE", "/callers/own/spec.json")
	stubKeyholderLaunch(t, map[string]string{"SSH_AUTH_SOCK": holder}, nil, nil)
	hand := stubStartHandoffExec(t, nil)

	if text, isErr := callStartChild(t, `{"child":"worker/kid","message":"carry on"}`); isErr {
		t.Fatalf("start: %s", text)
	}
	if hand.calls != 1 {
		t.Fatalf("hand-off ran %d time(s), want 1", hand.calls)
	}
	if v, _ := envValue(hand.env, "CLOWN_AGENT_SCOPE"); v != specPath {
		t.Errorf("CLOWN_AGENT_SCOPE = %q, want the child's own spec %q", v, specPath)
	}
	if v, _ := envValue(hand.env, "XDG_STATE_HOME"); v != os.Getenv("XDG_STATE_HOME") || v == "" {
		t.Errorf("XDG_STATE_HOME = %q, want the frontend's state directory %q", v, os.Getenv("XDG_STATE_HOME"))
	}
	if after, _ := os.ReadFile(specPath); string(after) != string(before) {
		t.Error("the child's stored spec was rewritten")
	}
}

// B-F2 for the start hand-off, switch ON: a confined child whose stored spec still
// binds the operator's agent is refused BEFORE a pair is started and before clown
// is called; the child stays stopped.
func TestStartHandoffOfAConfinedChildBindingTheOperatorsAgentIsRefusedWithTheSwitchOn(t *testing.T) {
	_, specPath := confinedKidFixture(t, `[{"path":"/run/user/1000/piggy-agent.sock","purpose":"piggy-agent"}]`, `{}`)
	before, _ := os.ReadFile(specPath)
	calls := stubKeyholderLaunch(t, map[string]string{"SSH_AUTH_SOCK": shortSocket(t)}, nil, nil)
	hand := stubStartHandoffExec(t, nil)

	text, isErr := callStartChild(t, `{"child":"worker/kid","message":"carry on"}`)
	if !isErr {
		t.Fatalf("a confined child binding the operator's agent was started: %s", text)
	}
	for _, want := range []string{"the operator's agent socket", "close this session and spawn a new one"} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal does not say %q: %s", want, text)
		}
	}
	if hand.calls != 0 {
		t.Errorf("the hand-off ran %d time(s) for a refused start", hand.calls)
	}
	if len(*calls) != 0 {
		t.Errorf("a pair was started for a refused start: %+v", *calls)
	}
	if after, _ := os.ReadFile(specPath); string(after) != string(before) {
		t.Error("the child's stored spec was rewritten")
	}
}

// B-F2 for the start hand-off, switch OFF: a confined child whose stored spec
// binds a holder socket is refused too (the hand-off would otherwise inherit the
// caller's environment and let clown relaunch on a path nothing serves).
func TestStartHandoffOfAConfinedChildBindingAHolderIsRefusedWithTheSwitchOff(t *testing.T) {
	confinedKidFixture(t, `[{"path":"/state/keyholder/gone/agent/agent.sock","purpose":"key-holder"}]`, `{"SSH_AUTH_SOCK":"/state/keyholder/gone/agent/agent.sock"}`)
	stubKeyholderLaunch(t, nil, nil, nil)
	hand := stubStartHandoffExec(t, nil)

	text, isErr := callStartChild(t, `{"child":"worker/kid","message":"carry on"}`)
	if !isErr {
		t.Fatalf("a confined child binding a holder was started with the switch off: %s", text)
	}
	if !strings.Contains(text, "a key holder socket") || !strings.Contains(text, "close this session and spawn a new one") {
		t.Errorf("the refusal does not name the change: %s", text)
	}
	if hand.calls != 0 {
		t.Errorf("the hand-off ran %d time(s) for a refused start", hand.calls)
	}
}

// coco's point 3: clown confines a started child by its sidecar record under the
// child's XDG_STATE_HOME, else by CLOWN_AGENT_SCOPE; an environment with neither
// starts a stopped confined session UNCONFINED. spinclass refuses such an
// environment before clown is called.
func TestCheckHandoffEnvRefusesAnEnvironmentClownCouldNotConfineFrom(t *testing.T) {
	const scope = "/state/spinclass/units/kid.spec.json"
	for _, tc := range []struct {
		name  string
		env   map[string]string
		scope string
		want  string // substring of the refusal, "" = accepted
	}{
		{"no XDG_STATE_HOME", map[string]string{"CLOWN_AGENT_SCOPE": scope}, scope, "XDG_STATE_HOME"},
		{"relative XDG_STATE_HOME", map[string]string{"XDG_STATE_HOME": "state", "CLOWN_AGENT_SCOPE": scope}, scope, "XDG_STATE_HOME"},
		{"confined child without its own scope", map[string]string{"XDG_STATE_HOME": "/state"}, scope, "CLOWN_AGENT_SCOPE"},
		{"confined child with the caller's scope", map[string]string{"XDG_STATE_HOME": "/state", "CLOWN_AGENT_SCOPE": "/callers/spec.json"}, scope, "CLOWN_AGENT_SCOPE"},
		{"both present", map[string]string{"XDG_STATE_HOME": "/state", "CLOWN_AGENT_SCOPE": scope}, scope, ""},
		{"unconfined child needs no scope", map[string]string{"XDG_STATE_HOME": "/state"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkHandoffEnv(tc.env, tc.scope)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("err = %v, want a refusal naming %s", err, tc.want)
			}
		})
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
