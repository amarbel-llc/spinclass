package main

// R3-L1: the stopped-session refusal for a confined caller reads st before the
// session lock is taken. A stop that lands between that read and the locked
// update must still refuse: the check is repeated on the locked copy.

import (
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

func TestAStopLandingBetweenTheReadAndTheLockedUpdateRefusesAConfinedRestart(t *testing.T) {
	f := newLiarFixture(t)
	stale := f.b.record(t) // read before the stop: StoppedAt is nil
	if stale.StoppedAt != nil {
		t.Fatal("the fixture session is already stopped")
	}
	// The stop lands after that read.
	now := time.Now().UTC()
	if err := session.Update(f.b.repo, "b-branch", func(cur *session.State) error {
		cur.StoppedAt, cur.StoppedBy, cur.StoppedReason = &now, session.OperatorRestartRequester, "operator stop"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := queueRestartPostureAs(stale, f.b.principal, f.b.principal, "back", false, "", true)
	if err == nil || !strings.Contains(err.Error(), "cannot act on a stopped session") {
		t.Fatalf("restart over a stop that landed after the read: err = %v, want the stopped-session refusal", err)
	}
	if rr := f.b.record(t).RestartRequest; rr != nil {
		t.Errorf("a restart was queued on the stopped session: %+v", rr)
	}
	if len(*f.handoff) != 0 {
		t.Errorf("the hand-off ran for a stopped session: %v", *f.handoff)
	}
}
