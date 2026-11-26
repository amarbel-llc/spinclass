package main

// B-M1: the nine grit tools classify their caller through the same evidence as
// the gated tools (serveCallerConfined), so the window between a confined launch
// (spinclass wrote the launch spec) and clown's unit record is closed for them
// too, and a session record that exists and cannot be read refuses. These tests
// put the REAL classifier back and go through a REGISTERED grit handler.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/store"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// gritAnswerAndCaller calls grit_stage add through its registered handler from a
// directory that is no session worktree, and reports how the caller classified.
func gritAnswerAndCaller(t *testing.T) (store.Session, string, bool) {
	t.Helper()
	t.Chdir(t.TempDir())
	// Record the caller the REGISTERED handler resolves (the real resolver, wrapped).
	original := gritCaller
	var caller store.Session
	t.Cleanup(func() { gritCaller = original })
	gritCaller = func(ctx context.Context, dir string) store.Session {
		caller = original(ctx, dir)
		return caller
	}
	cmd, ok := buildApp().GetCommand("grit_stage")
	if !ok || cmd.Run == nil {
		t.Fatal("grit_stage is not a registered MCP tool")
	}
	res, err := cmd.Run(context.Background(), json.RawMessage(`{"subcommand":"add","paths":["x"]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	return caller, res.Text, res.IsErr
}

func TestAGritToolIsConfinedInTheWindowBetweenTheLaunchAndClownsUnitRecord(t *testing.T) {
	f := newLiarFixture(t)
	asServeOfSession(t)
	state := os.Getenv("XDG_STATE_HOME")
	writeAt(t, state, "units/"+filepath.Base(confinement.UnitSpecPath(state, f.b.wt)), `{"version":1}`)

	caller, _, _ := gritAnswerAndCaller(t)
	if caller.Confinement != store.ConfinementConfined {
		t.Fatalf("grit caller in the window: %v, want confined", caller.Confinement)
	}
}

func TestAGritToolIsConfinedWhenTheFrontendSessionRecordCannotBeRead(t *testing.T) {
	f := newLiarFixture(t)
	asServeOfSession(t)
	// The record exists and cannot be parsed: unreadable evidence refuses.
	if err := os.WriteFile(session.RecordPath(f.b.wt), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := os.Getenv("XDG_STATE_HOME")
	writeAt(t, state, "units/"+filepath.Base(confinement.UnitSpecPath(state, f.b.wt)), `{"version":1}`)

	caller, _, _ := gritAnswerAndCaller(t)
	if caller.Confinement != store.ConfinementConfined {
		t.Fatalf("grit caller with an unreadable session record: %v, want confined", caller.Confinement)
	}
}

func TestAGritToolBehavesAsOnMasterWhenNothingSaysConfined(t *testing.T) {
	newLiarFixture(t)
	asServeOfSession(t)

	caller, _, _ := gritAnswerAndCaller(t)
	if caller.Confinement != store.ConfinementUnconfined {
		t.Fatalf("grit caller with no confinement evidence: %v, want unconfined (master behaviour)", caller.Confinement)
	}
}
