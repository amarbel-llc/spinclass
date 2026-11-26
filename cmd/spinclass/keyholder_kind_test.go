package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

func khKindRecorded(t *testing.T, key string) string {
	t.Helper()
	k, _ := keyholder.ReadKind(keyholderStateBaseFn(), key)
	return k
}

// A session first launched as a worker stays a worker when it is later resumed
// by the operator at his terminal: how the process is invoked does not change
// what the session is (B-M3).
func TestARecordedWorkerStaysAWorkerWhenLaterResumedAtATerminal(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	f := newKhFake(t)
	khUseFake(t, f)

	khLaunchSeams(t, false, "") // a script: not a terminal
	kh, _, err := keyholderLaunchEnvUndo(home, repo, "repo/kindw", false, &bytes.Buffer{})
	if err != nil || kh["SSH_AUTH_SOCK"] == "" {
		t.Fatalf("first launch (worker): got (%v, %v), want the holder's environment", kh, err)
	}
	if got := khKindRecorded(t, "repo/kindw"); got != keyholder.KindWorker {
		t.Fatalf("recorded kind = %q, want %q", got, keyholder.KindWorker)
	}

	khLaunchSeams(t, true, "") // the operator's terminal, no session ancestry: a driver-looking launch
	kh, _, err = keyholderLaunchEnvUndo(home, repo, "repo/kindw", false, &bytes.Buffer{})
	if err != nil || kh["SSH_AUTH_SOCK"] == "" {
		t.Fatalf("later launch at a terminal: got (%v, %v), want the holder's environment still", kh, err)
	}
}

// And a session first launched as a driver stays a driver when it is later
// resumed from inside a session: it keeps the operator's agent and gets no pair.
func TestARecordedDriverStaysADriverWhenLaterResumedFromInsideASession(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	f := newKhFake(t)
	built := khUseFake(t, f)

	khLaunchSeams(t, true, "")
	kh, undo, err := keyholderLaunchEnvUndo(home, repo, "repo/kindd", false, &bytes.Buffer{})
	if err != nil || kh != nil || undo != nil {
		t.Fatalf("first launch (driver): got (%v, undo=%v, %v), want (nil, nil, nil)", kh, undo != nil, err)
	}
	if got := khKindRecorded(t, "repo/kindd"); got != keyholder.KindDriver {
		t.Fatalf("recorded kind = %q, want %q", got, keyholder.KindDriver)
	}

	khLaunchSeams(t, true, "CLOWN_SESSION_ID") // now invoked from inside a session: worker-looking
	kh, undo, err = keyholderLaunchEnvUndo(home, repo, "repo/kindd", false, &bytes.Buffer{})
	if err != nil || kh != nil || undo != nil {
		t.Fatalf("later launch inside a session: got (%v, undo=%v, %v), want the driver's untouched environment", kh, undo != nil, err)
	}
	if *built != 0 || len(f.started) != 0 {
		t.Errorf("a recorded driver was given a pair: launcher built %d times, units %v", *built, f.started)
	}
}

// A spawned session is a worker whatever a record says: the spawner wrote its
// record of spawning and the session cannot.
func TestASpawnedSessionIsAWorkerWhateverItsKindRecordSays(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	f := newKhFake(t)
	khUseFake(t, f)
	if _, err := keyholder.RecordKind(keyholderStateBaseFn(), "repo/kinds", keyholder.KindDriver); err != nil {
		t.Fatal(err)
	}
	khLaunchSeams(t, true, "")
	kh, _, err := keyholderLaunchEnvUndo(home, repo, "repo/kinds", true, &bytes.Buffer{})
	if err != nil || kh["SSH_AUTH_SOCK"] == "" {
		t.Fatalf("spawned launch with a driver record: got (%v, %v), want the holder's environment", kh, err)
	}
}

// With the switch off nothing is written: a stopped switch leaves no kind record.
func TestNoKindIsRecordedWithTheSwitchOff(t *testing.T) {
	home, repo := khHome(t, "")
	f := newKhFake(t)
	khUseFake(t, f)
	khLaunchSeams(t, true, "")
	if _, _, err := keyholderLaunchEnvUndo(home, repo, "repo/kindoff", false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(keyholder.KindPath(keyholderStateBaseFn(), "repo/kindoff")); !os.IsNotExist(err) {
		t.Errorf("a kind record exists with the switch off: %v", err)
	}
}

// A session first launched before the record existed (or while the switch was
// off) has none: it is classified from its first launch with the switch on, and
// that is then written down. This is the documented fallback.
func TestASessionWithNoRecordIsClassifiedFromItsFirstLaunchAndThenRecorded(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	f := newKhFake(t)
	khUseFake(t, f)
	if got := khKindRecorded(t, "repo/kindn"); got != "" {
		t.Fatalf("kind before any launch = %q, want none", got)
	}
	khLaunchSeams(t, true, "")
	if _, _, err := keyholderLaunchEnvUndo(home, repo, "repo/kindn", false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := khKindRecorded(t, "repo/kindn"); got != keyholder.KindDriver {
		t.Errorf("recorded kind after a terminal launch = %q, want %q", got, keyholder.KindDriver)
	}
}

// The relaunch boundary check asks about the SESSION's kind, not the process
// that runs the check: a restart preflight runs inside a session (CLOWN_SESSION_ID
// set), which used to make every non-spawned session look like a worker with a
// pair to start.
func TestTheBoundaryCheckReadsTheRecordedKindNotTheCheckingProcess(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	if _, err := keyholder.RecordKind(keyholderStateBaseFn(), "repo/kindb", keyholder.KindDriver); err != nil {
		t.Fatal(err)
	}
	khLaunchSeams(t, false, "CLOWN_SESSION_ID") // a worker-looking checking process
	on, err := keyholderWantsPair(home, repo, "repo/kindb", false)
	if err != nil || on {
		t.Fatalf("a recorded driver, checked from inside a session: wants a pair = %v, %v; want false", on, err)
	}
	if _, err := keyholder.RecordKind(keyholderStateBaseFn(), "repo/kindc", keyholder.KindWorker); err != nil {
		t.Fatal(err)
	}
	khLaunchSeams(t, true, "") // a driver-looking checking process
	on, err = keyholderWantsPair(home, repo, "repo/kindc", false)
	if err != nil || !on {
		t.Fatalf("a recorded worker, checked at a terminal: wants a pair = %v, %v; want true", on, err)
	}
}

func TestTheKindFileIsOneWordAndOwnerOnly(t *testing.T) {
	_, _ = khHome(t, "")
	if _, err := keyholder.RecordKind(keyholderStateBaseFn(), "repo/kinde", keyholder.KindWorker); err != nil {
		t.Fatal(err)
	}
	p := keyholder.KindPath(keyholderStateBaseFn(), "repo/kinde")
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("kind file: %v %v, want mode 0600", fi, err)
	}
	b, _ := os.ReadFile(p)
	if strings.TrimSpace(string(b)) != keyholder.KindWorker {
		t.Errorf("kind file holds %q", b)
	}
}
