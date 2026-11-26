package main

// R3-M1: the gated tools (spawn-session, merge, grit, ...) classify through the
// same frontend evidence as the session verbs. The window the session verbs
// closed (spinclass has written the confined launch spec, clown has written no
// unit record yet) must be closed for them too: before this fix "no unit record
// names this principal" read as unconfined there, so a confined caller reaching
// the serve socket first could spawn or push. These tests put the REAL classifier
// back and write real files in a throwaway state directory.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// asServeOfSession makes this process the recorded serve of the fixture's
// session B (CLOWN_SESSION_ID and SPINCLASS_SESSION_ID are B's already), with the
// real gated-tool classifier back.
func asServeOfSession(t *testing.T) {
	t.Helper()
	useRealServeClassifier(t)
	removeRecord, err := registerServeRecord()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(removeRecord)
}

// spawnSessionAnswer calls spawn-session through its registered handler with
// arguments that fail the tool's own validation, so an unconfined caller is shown
// to get past the gate ("brief is required") without the tool doing anything.
func spawnSessionAnswer(t *testing.T) (string, bool) {
	t.Helper()
	return runHandler(handleSpawnSession)(context.Background(), json.RawMessage(`{"repo":"somewhere"}`))
}

func TestAGatedToolRefusesInTheWindowBetweenTheLaunchAndClownsUnitRecord(t *testing.T) {
	f := newLiarFixture(t)
	asServeOfSession(t)
	state := os.Getenv("XDG_STATE_HOME")
	// spinclass has written the confined launch spec for B's worktree; clown has
	// written no unit record yet, and the principal mapping still says unconfined.
	writeAt(t, state, "units/"+filepath.Base(confinement.UnitSpecPath(state, f.b.wt)), `{"version":1}`)

	text, isErr := spawnSessionAnswer(t)
	if !isErr || !strings.Contains(text, "classifies as confined") || !strings.Contains(text, "launched this session confined") {
		t.Fatalf("spawn-session in the window: isErr=%v %q, want a refusal naming the confined launch", isErr, text)
	}
}

func TestAGatedToolRefusesWhenAUnitRecordExistsAndCannotBeRead(t *testing.T) {
	newLiarFixture(t)
	asServeOfSession(t)
	// A unit record that does not parse is evidence that exists and cannot be
	// read: refuse (this already failed closed on master; pinned here).
	writeAt(t, os.Getenv("XDG_STATE_HOME"), "units/bad.json", "{not json")

	text, isErr := spawnSessionAnswer(t)
	if !isErr || !strings.Contains(text, "classifies as confined") {
		t.Fatalf("spawn-session with an unreadable unit record: isErr=%v %q, want a refusal", isErr, text)
	}
}

func TestAGatedToolBehavesAsOnMasterWhenNothingSaysConfined(t *testing.T) {
	newLiarFixture(t)
	asServeOfSession(t)

	text, isErr := spawnSessionAnswer(t)
	if !isErr || strings.Contains(text, "classifies as confined") || !strings.Contains(text, "brief is required") {
		t.Fatalf("spawn-session with no confinement evidence: isErr=%v %q, want the tool's own validation (master behaviour)", isErr, text)
	}
}

// The launch evidence is read from the FRONTEND's own files, never from the
// worktree's copy: with the frontend's session record unparsable (the session
// index skips it, so the session is not found by key), the frontend's principal
// mapping, also host-side, still names the worktree and the window stays closed.
func TestAGatedToolRefusesInTheWindowEvenWhenTheFrontendSessionRecordCannotBeRead(t *testing.T) {
	f := newLiarFixture(t)
	asServeOfSession(t)
	state := os.Getenv("XDG_STATE_HOME")
	if err := os.WriteFile(session.RecordPath(f.b.wt), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAt(t, state, "units/"+filepath.Base(confinement.UnitSpecPath(state, f.b.wt)), `{"version":1}`)

	text, isErr := spawnSessionAnswer(t)
	if !isErr || !strings.Contains(text, "classifies as confined") {
		t.Fatalf("spawn-session in the window with an unreadable session record: isErr=%v %q, want a refusal", isErr, text)
	}
}
