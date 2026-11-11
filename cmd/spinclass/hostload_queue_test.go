package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/hostqueue"
)

// #424 step 5: host-load's queue section.

func stubHostLoadLedger(t *testing.T) hostqueue.Ledger {
	t.Helper()
	dir := t.TempDir()
	l := hostqueue.Ledger{Dir: filepath.Join(dir, "run"), HoldPath: filepath.Join(dir, "state", "hold")}
	old := hostLoadLedger
	t.Cleanup(func() { hostLoadLedger = old })
	hostLoadLedger = func() (hostqueue.Ledger, error) { return l, nil }
	return l
}

// An empty ledger adds nothing, so a host that never queued renders as before.
func TestHostQueueViewEmptyIsNil(t *testing.T) {
	stubHostLoadLedger(t)
	if v := hostQueueView(); v != nil {
		t.Fatalf("= %+v, want nil", v)
	}
}

func TestHostQueueViewAndFormat(t *testing.T) {
	l := stubHostLoadLedger(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	release, err := l.Admit(hostqueue.Marker{Session: "r/gate", Kind: "merge", AdmittedAt: now.Add(-3 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, leave, err := l.Wait(hostqueue.Marker{Session: "r/waiter", Kind: "check", WaitingSince: now.Add(-90 * time.Second), Reason: "waiting for load"})
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	if err := l.SetHold("deploy"); err != nil {
		t.Fatal(err)
	}
	v := hostQueueView()
	if v == nil {
		t.Fatal("nil view with a gate, a waiter and a hold")
	}
	got := formatHostQueue(v, now)
	for _, want := range []string{
		"host queue: 1 gate(s) running, 1 waiting, HELD: deploy",
		"gate r/gate merge for 3m0s",
		"wait r/waiter check for 1m30s: waiting for load",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("format =\n%s\nwant a line %q", got, want)
		}
	}
}
