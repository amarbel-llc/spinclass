package main

import (
	"bytes"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
)

// khLaunchSeams sets what the launch classifier sees: a terminal on stdin and
// stdout or not, and which session marker (if any) is in the environment.
func khLaunchSeams(t *testing.T, terminals bool, marker string) {
	t.Helper()
	prevT, prevM := launchTerminals, profileGrantAgentRuntime
	t.Cleanup(func() { launchTerminals, profileGrantAgentRuntime = prevT, prevM })
	launchTerminals = func() bool { return terminals }
	profileGrantAgentRuntime = func() string { return marker }
}

const khOperatorAgent = "/operator/agent.sock"

func khOperatorEnv() map[string]string {
	return map[string]string{"SSH_AUTH_SOCK": khOperatorAgent, "PIGGY_AUTH_SOCK": "/operator/piggy.sock"}
}

// A driver: the operator at a terminal, no session ancestry, not a spawned
// session. He keeps his agent and no pair is started.
func TestKeyholderDriverAtATerminalKeepsTheOperatorsAgentAndStartsNoPair(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	f := newKhFake(t)
	built := khUseFake(t, f)
	khLaunchSeams(t, true, "")
	kh, undo, err := keyholderLaunchEnvUndo(home, repo, "repo/driver", false, &bytes.Buffer{})
	if err != nil || kh != nil || undo != nil {
		t.Fatalf("driver launch: got (%v, undo=%v, %v), want (nil, nil, nil)", kh, undo != nil, err)
	}
	if *built != 0 || len(f.started) != 0 {
		t.Errorf("a driver launch built a launcher (%d) or started units (%v)", *built, f.started)
	}
	got := withKeyholderEnv(khOperatorEnv(), kh)
	if got["SSH_AUTH_SOCK"] != khOperatorAgent || got["PIGGY_AUTH_SOCK"] != "/operator/piggy.sock" {
		t.Errorf("a driver's environment changed: %v", got)
	}
}

// Every launch that is not the operator's own gets the holder.
func TestKeyholderLaunchThatIsNotTheOperatorsOwnGetsTheHolder(t *testing.T) {
	for name, tc := range map[string]struct {
		terminals bool
		marker    string
		spawned   bool
	}{
		"not at a terminal":                   {terminals: false},
		"inside a clown session":              {terminals: true, marker: "CLOWN_SESSION_ID"},
		"inside a spinclass session":          {terminals: true, marker: "SPINCLASS_SESSION_ID"},
		"a spawned worker resumed by a human": {terminals: true, spawned: true},
		"a spawned worker, no terminal":       {terminals: false, spawned: true},
	} {
		t.Run(name, func(t *testing.T) {
			home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
			f := newKhFake(t)
			khUseFake(t, f)
			khLaunchSeams(t, tc.terminals, tc.marker)
			kh, _, err := keyholderLaunchEnvUndo(home, repo, "repo/worker", tc.spawned, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(kh["SSH_AUTH_SOCK"], "agent.sock") || kh["SSH_AUTH_SOCK"] == khOperatorAgent {
				t.Fatalf("SSH_AUTH_SOCK = %q, want the session's holder socket", kh["SSH_AUTH_SOCK"])
			}
			if len(f.started) != 2 {
				t.Errorf("units started = %v, want the pair", f.started)
			}
			if got := withKeyholderEnv(khOperatorEnv(), kh); got["SSH_AUTH_SOCK"] == khOperatorAgent {
				t.Errorf("the worker still has the operator's agent: %v", got)
			}
		})
	}
}

// The spawn path never asks who is at the keyboard: a spawned worker gets the
// holder even when its launcher looks exactly like the operator's own (a
// terminal and no session markers), so a worker cannot make itself a driver.
func TestSpawnedWorkerGetsTheHolderEvenWhenTheLauncherLooksLikeADriver(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	f := newKhFake(t)
	khUseFake(t, f)
	khLaunchSeams(t, true, "") // driver-looking: a terminal and no markers
	if spawn.KeyholderEnv == nil {
		t.Fatal("the spawn path has no key holder hook wired")
	}
	kh, _, err := spawn.KeyholderEnv(home, repo, "repo/spawned")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(kh["SSH_AUTH_SOCK"], keyholder.Hash("repo/spawned")+"/agent/agent.sock") {
		t.Fatalf("a spawned worker got SSH_AUTH_SOCK %q, want its own holder", kh["SSH_AUTH_SOCK"])
	}
	if len(f.started) != 2 {
		t.Errorf("units started = %v, want the pair", f.started)
	}
}

// Off means off: a driver or a worker, nothing starts.
func TestKeyholderOffStartsNothingForAnyLaunchKind(t *testing.T) {
	for _, spawned := range []bool{false, true} {
		home, repo := khHome(t, "")
		f := newKhFake(t)
		built := khUseFake(t, f)
		khLaunchSeams(t, false, "")
		kh, undo, err := keyholderLaunchEnvUndo(home, repo, "repo/a", spawned, &bytes.Buffer{})
		if err != nil || kh != nil || undo != nil || *built != 0 || len(f.started) != 0 {
			t.Errorf("spawned=%v: off switch gave (%v, %v, %v), built=%d, started=%v", spawned, kh, undo != nil, err, *built, f.started)
		}
	}
}

// The classification, pinned apart from what is done with it: who counts as the
// operator's own launch.
func TestClassifyKeyholderLaunchPinsWhoIsTheDriver(t *testing.T) {
	for name, tc := range map[string]struct {
		terminals bool
		marker    string
		spawned   bool
		want      launchKind
	}{
		"operator at a terminal, no ancestry":      {terminals: true, want: launchDriver},
		"no terminal":                              {terminals: false, want: launchWorker},
		"inside a clown session at a terminal":     {terminals: true, marker: "CLOWN_SESSION_ID", want: launchWorker},
		"inside a spinclass session at a terminal": {terminals: true, marker: "SPINCLASS_SESSION_ID", want: launchWorker},
		"spawned record at a terminal":             {terminals: true, spawned: true, want: launchWorker},
	} {
		t.Run(name, func(t *testing.T) {
			khLaunchSeams(t, tc.terminals, tc.marker)
			if got := classifyKeyholderLaunch(tc.spawned); got != tc.want {
				t.Errorf("classifyKeyholderLaunch = %v, want %v", got, tc.want)
			}
		})
	}
}

// The policy table, apart from the classification: today (rule A) a driver gets
// no pair and no environment rewrite and a worker gets both. Rule B, a fast
// follow, flips only StartsPair for the driver.
func TestKeyholderPolicyForEachKind(t *testing.T) {
	if got := keyholderPolicyFor(launchDriver); got != (keyholderLaunchPolicy{}) {
		t.Errorf("driver policy = %+v, want no pair and no env rewrite (rule A)", got)
	}
	if got := keyholderPolicyFor(launchWorker); got != (keyholderLaunchPolicy{StartsPair: true, RewritesEnv: true}) {
		t.Errorf("worker policy = %+v, want a pair and an env rewrite", got)
	}
}

// The additive step rule B needs: a policy that starts the pair but leaves the
// environment alone starts exactly the pair, hands back its undo, and returns no
// environment, so the operator's agent stays his.
func TestApplyKeyholderPolicyPairBesideTheOperatorsAgentLeavesTheEnvironmentAlone(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	f := newKhFake(t)
	khUseFake(t, f)
	kh, undo, err := applyKeyholderPolicy(keyholderLaunchPolicy{StartsPair: true}, home, repo, "repo/b", &bytes.Buffer{})
	if err != nil || kh != nil {
		t.Fatalf("got (%v, %v), want no environment and no error", kh, err)
	}
	if undo == nil || len(f.started) != 2 {
		t.Errorf("the pair was not started and made undoable: undo=%v started=%v", undo != nil, f.started)
	}
	if got := withKeyholderEnv(khOperatorEnv(), kh); got["SSH_AUTH_SOCK"] != khOperatorAgent {
		t.Errorf("the operator's agent was replaced: %v", got)
	}
}

// A resume is classified by the SESSION's record: one whose record names a
// spawner is a worker for the pair even when the operator resumes it at a
// terminal.
func TestResumeIsSpawnedWhenTheRecordNamesASpawner(t *testing.T) {
	if resumeIsSpawned(&session.State{}) {
		t.Error("a record with no spawner counted as spawned")
	}
	for name, st := range map[string]*session.State{
		"principal": {SpawnedByPrincipal: "p-1"},
		"legacy":    {SpawnedBy: "repo/driver"},
	} {
		if !resumeIsSpawned(st) {
			t.Errorf("%s: a record naming a spawner did not count as spawned", name)
		}
	}
}
