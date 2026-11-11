package check

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"code.linenisgreat.com/spinclass/internal/0/hostload"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/hostqueue"
)

// #424 step 3: the admission wait in runHookPhase.

func healthySnap() hostload.Snapshot {
	return hostload.Snapshot{CPUs: 8, LoadPerCPU: 0.2, MemAvailable: 10 << 30, HasPressure: true, HasStoreFree: true, StoreFreeBytes: 100 << 30}
}

// stubHostQueue turns the queue on with a fast policy, a temp ledger and the
// given probe; it returns the ledger.
func stubHostQueue(t *testing.T, cfg hostqueue.Config, probe func() (hostload.Snapshot, error)) hostqueue.Ledger {
	t.Helper()
	dir := t.TempDir()
	ledger := hostqueue.Ledger{Dir: filepath.Join(dir, "run"), HoldPath: filepath.Join(dir, "state", "hold")}
	oldCfg, oldLedger, oldProbe, oldBeat := hostQueueConfig, hostQueueLedger, hostQueueProbe, hostQueueHeartbeat
	t.Cleanup(func() {
		hostQueueConfig, hostQueueLedger, hostQueueProbe, hostQueueHeartbeat = oldCfg, oldLedger, oldProbe, oldBeat
	})
	hostQueueConfig = func(sweatfile.Hierarchy, string) (hostqueue.Config, bool, error) { return cfg, true, nil }
	hostQueueLedger = func() (hostqueue.Ledger, error) { return ledger, nil }
	hostQueueProbe = probe
	hostQueueHeartbeat = 0
	return ledger
}

func fastConfig() hostqueue.Config {
	cfg := hostqueue.DefaultConfig()
	cfg.Settle, cfg.Poll, cfg.MaxWait, cfg.IdleGrace = 0, time.Millisecond, 5*time.Second, 0
	return cfg
}

func admit(t *testing.T, ctx context.Context) (func(), error, string, string) {
	t.Helper()
	var out, activity bytes.Buffer
	rep := crap.NewReporter(&out, crap.ReporterOptions{})
	release, err := admitHostSlot(ctx, rep, sweatfile.Hierarchy{}, t.TempDir(), &activity)
	return release, err, out.String(), activity.String()
}

// With no admission policy (no [host-queue] section) nothing is read and
// nothing is written: the gate's output is unchanged.
func TestHostQueueOffTouchesNothing(t *testing.T) {
	old := hostQueueLedger
	t.Cleanup(func() { hostQueueLedger = old })
	hostQueueLedger = func() (hostqueue.Ledger, error) {
		t.Fatal("the ledger was opened with the host queue off")
		return hostqueue.Ledger{}, nil
	}
	release, err, out, activity := admit(t, context.Background())
	if err != nil || out != "" || activity != "" {
		t.Fatalf("= (%v, %q, %q), want nothing", err, out, activity)
	}
	release()
}

func TestHostQueueAdmitsAtOnceAndReleases(t *testing.T) {
	ledger := stubHostQueue(t, fastConfig(), func() (hostload.Snapshot, error) { return healthySnap(), nil })
	release, err, out, _ := admit(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "host queue") {
		t.Errorf("an unwaited admission must stay silent, got %q", out)
	}
	if live, _ := ledger.Live(false); len(live) != 1 {
		t.Fatalf("live = %+v, want this gate's marker", live)
	}
	release()
	if live, _ := ledger.Live(false); len(live) != 0 {
		t.Fatalf("live after release = %+v", live)
	}
}

func TestHostQueueWaitsForLoadThenAdmits(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	stubHostQueue(t, fastConfig(), func() (hostload.Snapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		s := healthySnap()
		if calls <= 2 {
			s.LoadPerCPU = 3
		}
		return s, nil
	})
	release, err, out, activity := admit(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !strings.Contains(out, "host queue wait") || !strings.Contains(out, "(load)") {
		t.Errorf("output = %q, want the wait point naming load", out)
	}
	if !strings.Contains(activity, "host queue: waiting for load") {
		t.Errorf("activity = %q, want a heartbeat naming the reason", activity)
	}
}

// A gate slot held by another holder keeps the waiter waiting until it is
// released.
func TestHostQueueWaitsForARunningGate(t *testing.T) {
	ledger := stubHostQueue(t, fastConfig(), func() (hostload.Snapshot, error) { return healthySnap(), nil })
	unlock, err := ledger.Lock()
	if err != nil {
		t.Fatal(err)
	}
	other, err := ledger.Admit(hostqueue.Marker{Session: "r/other", AdmittedAt: time.Now()})
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		other()
	}()
	release, err, out, activity := admit(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !strings.Contains(out, "(gates)") || !strings.Contains(activity, "r/other") {
		t.Errorf("output %q / activity %q, want the gate reason naming r/other", out, activity)
	}
}

func TestHostQueueTimesOutWithTheReason(t *testing.T) {
	cfg := fastConfig()
	cfg.MaxWait = 30 * time.Millisecond
	stubHostQueue(t, cfg, func() (hostload.Snapshot, error) {
		s := healthySnap()
		s.MemAvailable = 1 << 30
		return s, nil
	})
	_, err, out, _ := admit(t, context.Background())
	if !errors.Is(err, ErrHostQueueTimeout) || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("err = %v, want a host queue timeout naming memory", err)
	}
	if !strings.Contains(out, "host queue") {
		t.Errorf("output = %q, want the failed host queue phase", out)
	}
}

func TestHostQueueWaitIsCancellable(t *testing.T) {
	stubHostQueue(t, fastConfig(), func() (hostload.Snapshot, error) {
		s := healthySnap()
		s.LoadPerCPU = 9
		return s, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err, _, _ := admit(t, ctx)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v, want a cancellation", err)
	}
}

// A probe that cannot read the host admits with a warning: the queue must not
// wedge every gate.
func TestHostQueueProbeFailureAdmitsWithAWarning(t *testing.T) {
	stubHostQueue(t, fastConfig(), func() (hostload.Snapshot, error) { return hostload.Snapshot{}, errors.New("no /proc") })
	release, err, out, _ := admit(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if !strings.Contains(out, "warning: host queue") || !strings.Contains(out, "no /proc") {
		t.Errorf("output = %q, want the warning", out)
	}
}

// The operator's hold keeps a waiter waiting, and its reason is the wait reason.
func TestHostQueueHoldIsTheWaitReason(t *testing.T) {
	cfg := fastConfig()
	cfg.MaxWait = 30 * time.Millisecond
	ledger := stubHostQueue(t, cfg, func() (hostload.Snapshot, error) { return healthySnap(), nil })
	if err := ledger.SetHold("relock in progress"); err != nil {
		t.Fatal(err)
	}
	_, err, _, activity := admit(t, context.Background())
	if !errors.Is(err, ErrHostQueueTimeout) || !strings.Contains(err.Error(), "relock in progress") {
		t.Fatalf("err = %v, want a timeout naming the hold", err)
	}
	if !strings.Contains(activity, "held by the operator: relock in progress") {
		t.Errorf("activity = %q", activity)
	}
}
