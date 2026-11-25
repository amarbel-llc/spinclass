package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// markKidConfined makes worker/kid's worktree a confined one: a frontend unit
// record and a stored unit spec binding the given sockets (a JSON array), under
// the test's state directory. It returns the spec path.
func markKidConfined(t *testing.T, wt, sockets, env string) string {
	t.Helper()
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		t.Fatal("the fixture left no XDG_STATE_HOME")
	}
	rec := confinement.UnitRecordPath(stateDir, wt)
	if err := os.MkdirAll(filepath.Dir(rec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte(`{"principal":"p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := confinement.UnitSpecPath(stateDir, wt)
	// A holder spec as the builder writes it also binds the certificate directory
	// read-only.
	bindsRO := `[]`
	if strings.Contains(sockets, `"key-holder"`) {
		bindsRO = `["/state/keyholder/kid/cert"]`
	}
	stored := `{"version":1,"worktree":"/w","operator_home":"/h","result_path":"/r","binds_rw":[],"binds_ro":` + bindsRO + `,"deny_paths":[],"sockets":` + sockets + `,"env":` + env + `}`
	if err := os.WriteFile(spec, []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	return spec
}

const (
	boundaryOperatorSockets = `[{"path":"/run/user/1000/piggy-agent.sock","purpose":"piggy-agent"}]`
	boundaryHolderSockets   = `[{"path":"/state/keyholder/kid/agent/agent.sock","purpose":"key-holder"}]`
	// What the builder writes beside each: the holder spec names its socket as
	// SSH_AUTH_SOCK, the off spec names no agent variable.
	boundaryHolderEnv = `{"SSH_AUTH_SOCK":"/state/keyholder/kid/agent/agent.sock","SPINCLASS_KEYHOLDER_CERT":"/state/keyholder/kid/cert/cert.pub"}`
	boundaryOffEnv    = `{}`
)

// B-F2 / A-H1 on the restart path, switch ON: clown relaunches from the stored
// spec without `sc resume`, so the preflight is where a spec that still binds the
// operator's agent is stopped. The restart is refused, the request is cleared, no
// pair is started for it, and the spec still binds what it did (nothing is
// rewritten): the relaunch never happens, so the operator's socket is never
// rebound to a session now meant to run on its own key.
func TestRestartPreflightRefusesAConfinedSessionWhoseSpecBindsTheOperatorsAgentWithTheSwitchOn(t *testing.T) {
	stubScheduleRestartWatch(t, nil)
	stubNotifyPrincipal(t)
	r := khRemintEnv(t)
	repoPath := khSessionOn(t)
	wt := readKid(t, repoPath).WorktreePath
	spec := markKidConfined(t, wt, boundaryOperatorSockets, boundaryOffEnv)
	before, _ := os.ReadFile(spec)
	if text, isErr := callRestartThis(t, `{"reason":"why"}`); isErr {
		t.Fatalf("queueing the restart: %s", text)
	}

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "refused" {
		t.Fatalf("preflight = %+v, %v; want a refusal", v, err)
	}
	for _, want := range []string{"the operator's agent socket", "[keyholder] mode", "close this session and spawn a new one", "nothing was restarted"} {
		if !strings.Contains(v.Reason, want) {
			t.Errorf("the refusal does not say %q: %s", want, v.Reason)
		}
	}
	if len(r.started) != 0 {
		t.Errorf("a pair was started for a refused restart: %v", r.started)
	}
	if readKid(t, repoPath).RestartRequest != nil || readKid(t, repoPath).RestartingAt != nil {
		t.Error("a refused restart must clear the request and not mark the session restarting")
	}
	if after, _ := os.ReadFile(spec); string(after) != string(before) {
		t.Error("the stored spec was rewritten")
	}
}

// Switch OFF, spec written with the holder: the same refusal in the other
// direction.
func TestRestartPreflightRefusesAConfinedSessionWhoseSpecBindsAHolderWithTheSwitchOff(t *testing.T) {
	stubScheduleRestartWatch(t, nil)
	stubNotifyPrincipal(t)
	khRemintEnv(t)
	repoPath := khSessionOn(t)
	writeGlobalSweatfile(t, os.Getenv("HOME"), restartHandoffTOML+"[keyholder]\nmode = \"off\"\n")
	wt := readKid(t, repoPath).WorktreePath
	markKidConfined(t, wt, boundaryHolderSockets, boundaryHolderEnv)
	if text, isErr := callRestartThis(t, `{"reason":"why"}`); isErr {
		t.Fatalf("queueing the restart: %s", text)
	}

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "refused" {
		t.Fatalf("preflight = %+v, %v; want a refusal", v, err)
	}
	if !strings.Contains(v.Reason, "a key holder socket") || !strings.Contains(v.Reason, "close this session and spawn a new one") {
		t.Errorf("the refusal does not name the change: %s", v.Reason)
	}
}

// A spec that matches the switch restarts as before, and a session never
// launched confined is not looked at (the pair check alone applies).
func TestRestartPreflightRestartsAConfinedSessionWhoseSpecMatchesTheSwitch(t *testing.T) {
	stubScheduleRestartWatch(t, nil)
	stubNotifyPrincipal(t)
	khRemintEnv(t)
	repoPath := khSessionOn(t)
	wt := readKid(t, repoPath).WorktreePath
	markKidConfined(t, wt, boundaryHolderSockets, boundaryHolderEnv)
	khStart(t, repoPath, &bytes.Buffer{})
	if text, isErr := callRestartThis(t, `{"reason":"why"}`); isErr {
		t.Fatalf("queueing the restart: %s", text)
	}
	if v, err := runRestartPreflight("worker/kid"); err != nil || v.Verdict != "restart" {
		t.Fatalf("preflight = %+v, %v; want restart", v, err)
	}
}

// The re-arm shares the check: the switch flipped between the verdict and the
// re-arm refuses the restart it restores.
func TestRestartRearmRefusesWhenTheSwitchChangedUnderTheStoredSpec(t *testing.T) {
	stubScheduleRestartWatch(t, nil)
	stubNotifyPrincipal(t)
	khRemintEnv(t)
	repoPath := khSessionOn(t)
	wt := readKid(t, repoPath).WorktreePath
	markKidConfined(t, wt, boundaryHolderSockets, boundaryHolderEnv)
	khStart(t, repoPath, &bytes.Buffer{})
	if text, isErr := callRestartThis(t, `{"reason":"why"}`); isErr {
		t.Fatalf("queueing the restart: %s", text)
	}
	if v, err := runRestartPreflight("worker/kid"); err != nil || v.Verdict != "restart" {
		t.Fatalf("setup preflight = %+v, %v", v, err)
	}
	writeGlobalSweatfile(t, os.Getenv("HOME"), restartHandoffTOML+"[keyholder]\nmode = \"off\"\n")

	v, err := runRestartRearm("worker/kid")
	if err != nil || v.Verdict != "refused" || !strings.Contains(v.Reason, "close this session and spawn a new one") {
		t.Fatalf("rearm = %+v, %v; want a refusal naming a new session", v, err)
	}
}
