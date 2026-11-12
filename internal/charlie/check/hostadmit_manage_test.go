package check

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/hostload"
	"code.linenisgreat.com/spinclass/internal/alfa/hostqueue"
)

// spinclass#483 slice 3: a waiting gate takes management commands at its next
// look.

func sendWaiterCommand(t *testing.T, ledger hostqueue.Ledger, verb string) {
	t.Helper()
	waiting, err := ledger.Waiting(false)
	if err != nil || len(waiting) != 1 {
		t.Fatalf("waiting = %+v, %v; want one", waiting, err)
	}
	m := waiting[0]
	if _, err := ledger.SendCommand(hostqueue.Command{
		Verb: verb, Session: m.Session, Seq: m.Seq, PID: m.PID, WaitingSince: m.WaitingSince,
		At: time.Now().UTC(), Actor: "tty",
	}); err != nil {
		t.Fatal(err)
	}
}

// dumpLedger prints every file in the ledger directory with its content, so a
// command that was never taken can be compared with the marker it addressed.
func dumpLedger(t *testing.T, ledger hostqueue.Ledger) {
	t.Helper()
	entries, _ := os.ReadDir(ledger.Dir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(ledger.Dir, e.Name()))
		t.Logf("ledger file %s: %s", e.Name(), strings.TrimSpace(string(data)))
	}
}

func holdOneSlot(t *testing.T, ledger hostqueue.Ledger) (release func()) {
	t.Helper()
	unlock, err := ledger.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	release, err = ledger.Admit(hostqueue.Marker{Session: "r/blocker", AdmittedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func TestHostQueueCancelEndsTheWaitAndFreesThePlace(t *testing.T) {
	cfg := fastConfig()
	cfg.MaxWait = time.Minute
	ledger := stubHostQueue(t, cfg, func() (hostload.Snapshot, error) { return healthySnap(), nil })
	blocker := holdOneSlot(t, ledger)
	defer blocker()

	result := make(chan error, 1)
	go func() {
		_, _, _, err := admit(t, context.Background())
		result <- err
	}()
	waitForWaiters(t, ledger, 1)
	sendWaiterCommand(t, ledger, "cancel")
	select {
	case err := <-result:
		if !errors.Is(err, ErrHostQueueCancelled) {
			t.Fatalf("err = %v, want a cancelled wait", err)
		}
	case <-time.After(10 * time.Second):
		dumpLedger(t, ledger)
		t.Fatal("the cancel was never taken")
	}
	if w, _ := ledger.Waiting(false); len(w) != 0 {
		t.Errorf("a cancelled gate's place outlived it: %+v", w)
	}
}

func TestHostQueueAHeldGateWaitsUntilItIsReleased(t *testing.T) {
	cfg := fastConfig()
	cfg.MaxWait = time.Minute
	ledger := stubHostQueue(t, cfg, func() (hostload.Snapshot, error) { return healthySnap(), nil })
	blocker := holdOneSlot(t, ledger)

	type outcome struct {
		release func()
		err     error
	}
	result := make(chan outcome, 1)
	go func() {
		release, _, _, err := admit(t, context.Background())
		result <- outcome{release, err}
	}()
	waitForWaiters(t, ledger, 1)
	sendWaiterCommand(t, ledger, "hold")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if w, _ := ledger.Waiting(false); len(w) == 1 && w[0].Held {
			break
		}
		if time.Now().After(deadline) {
			dumpLedger(t, ledger)
			t.Fatal("the hold was never taken")
		}
		time.Sleep(time.Millisecond)
	}
	blocker() // the slot is free and the host healthy, but the gate is held
	select {
	case o := <-result:
		t.Fatalf("a held gate was admitted: %v", o.err)
	case <-time.After(150 * time.Millisecond):
	}
	sendWaiterCommand(t, ledger, "release")
	select {
	case o := <-result:
		if o.err != nil {
			t.Fatal(o.err)
		}
		o.release()
	case <-time.After(10 * time.Second):
		t.Fatal("a released gate was never admitted")
	}
}
