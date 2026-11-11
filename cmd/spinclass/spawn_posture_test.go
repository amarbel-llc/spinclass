package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
)

// spinclass#474: the spawn surfaces' posture option. The launch semantics are
// tested in internal/foxtrot/spawn, the permission floor in internal/charlie/perms.

func TestSpawnParamsLaunchOptionsParsesPosture(t *testing.T) {
	for _, c := range []struct {
		posture string
		want    spawn.Posture
	}{{"", spawn.PostureDefault}, {"unattended", spawn.PostureUnattended}, {"attended", spawn.PostureAttended}} {
		opts, err := spawnParams{Posture: c.posture, Confined: true}.launchOptions()
		if err != nil || opts.Posture != c.want || opts.Confine != spawn.ConfineRequire {
			t.Errorf("posture %q: = (%+v, %v), want posture %q and ConfineRequire", c.posture, opts, err, c.want)
		}
	}
	if _, err := (spawnParams{Posture: "keep"}).launchOptions(); err == nil {
		t.Error("posture keep: want a refusal, spawn has nothing to keep")
	}
}

// `sc spawn --posture attended` asks at the terminal: no terminal refuses, a
// non-y answer declines, both before anything is spawned.
func TestSpawnCLIAttendedNeedsATerminalAndAYes(t *testing.T) {
	stubUnconfinedGate(t, false, "y\n")
	args, _ := json.Marshal(map[string]any{"repo": "nowhere-such-repo", "brief": "b", "posture": "attended"})
	if err := runSpawnCLI(context.Background(), args); err == nil || !strings.Contains(err.Error(), "--posture attended needs a human at a terminal") {
		t.Fatalf("no terminal: got %v, want the terminal refusal", err)
	}
	_, stderr := stubUnconfinedGate(t, true, "n\n")
	if err := runSpawnCLI(context.Background(), args); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("declined: got %v, want the declined refusal", err)
	}
	if !strings.Contains(stderr.String(), "ATTENDED") {
		t.Errorf("prompt = %q, want it to name the attended posture", stderr.String())
	}
}

// A yes passes the prompt; the spawn then fails on the made-up repo, which
// shows the prompt did not stop it.
func TestSpawnCLIAttendedConfirmedProceedsToTheLaunch(t *testing.T) {
	stubUnconfinedGate(t, true, "y\n")
	args, _ := json.Marshal(map[string]any{"repo": "nowhere-such-repo", "brief": "b", "posture": "attended"})
	err := runSpawnCLI(context.Background(), args)
	if err == nil || strings.Contains(err.Error(), "not confirmed") || strings.Contains(err.Error(), "terminal") {
		t.Fatalf("got %v, want the launch's own error past the prompt", err)
	}
}

func TestAttendedSpawnNote(t *testing.T) {
	if got := attendedSpawnNote("attended", "r/b"); !strings.Contains(got, "sc resume r/b") {
		t.Errorf("attended note = %q, want the attach command", got)
	}
	for _, p := range []string{"", "unattended"} {
		if got := attendedSpawnNote(p, "r/b"); got != "" {
			t.Errorf("posture %q note = %q, want none", p, got)
		}
	}
}

func TestSpawnToolsDeclarePosture(t *testing.T) {
	for _, params := range [][]command.Param{spawnParamList(), spawnSessionParams()} {
		found := false
		for _, p := range params {
			if p.Name == "posture" {
				found = true
				if !strings.Contains(p.Description, "allow-posture-widening") {
					t.Errorf("posture description must name the widening key: %q", p.Description)
				}
			}
		}
		if !found {
			t.Error("spawn params lack posture")
		}
	}
}
