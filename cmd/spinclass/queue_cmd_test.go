package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/hostqueue"
)

// spinclass#483 slice 1: queue-status, queue-hold and queue-release.

func stubQueueLedger(t *testing.T) hostqueue.Ledger {
	t.Helper()
	dir := t.TempDir()
	l := hostqueue.Ledger{Dir: filepath.Join(dir, "run"), HoldPath: filepath.Join(dir, "state", "hold")}
	prevQ, prevH := queueLedger, hostLoadLedger
	queueLedger = func() (hostqueue.Ledger, error) { return l, nil }
	hostLoadLedger = queueLedger
	t.Cleanup(func() { queueLedger, hostLoadLedger = prevQ, prevH })
	return l
}

func stubOperatorTerminal(t *testing.T, tty bool) (stdout, stderr *bytes.Buffer) {
	t.Helper()
	pt, po, pe, pn := operatorHandleIsTerminal, operatorHandleStdout, operatorHandleStderr, operatorHandleNow
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	operatorHandleIsTerminal = func() bool { return tty }
	operatorHandleStdout = func() io.Writer { return stdout }
	operatorHandleStderr = func() io.Writer { return stderr }
	operatorHandleNow = func() time.Time { return time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC) }
	t.Cleanup(func() {
		operatorHandleIsTerminal, operatorHandleStdout, operatorHandleStderr, operatorHandleNow = pt, po, pe, pn
	})
	return stdout, stderr
}

func noBypass(t *testing.T) {
	t.Helper()
	prev := liveServeProcesses
	liveServeProcesses = func() []serveProcess { return nil }
	t.Cleanup(func() { liveServeProcesses = prev })
}

func TestQueueHoldAndReleaseAtATerminal(t *testing.T) {
	l := stubQueueLedger(t)
	noBypass(t)
	stdout, _ := stubOperatorTerminal(t, true)

	if err := runQueueHold(queueHoldParams{Reason: "  deploy window "}); err != nil {
		t.Fatal(err)
	}
	if held, why := l.Hold(); !held || why != "deploy window" {
		t.Fatalf("hold = %v %q", held, why)
	}
	status, err := runQueueStatus(queueStatusParams{})
	if err != nil || !strings.Contains(status, "HELD: deploy window") {
		t.Fatalf("status = %q (%v), want the hold", status, err)
	}
	if err := runQueueRelease(); err != nil {
		t.Fatal(err)
	}
	if held, _ := l.Hold(); held {
		t.Fatal("still held")
	}
	if status, _ = runQueueStatus(queueStatusParams{}); !strings.HasPrefix(status, "host queue: empty") {
		t.Errorf("status = %q, want empty", status)
	}
	if !strings.Contains(stdout.String(), "host queue held") || !strings.Contains(stdout.String(), "host queue released") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestQueueVerbsRefuseWithoutATerminalAndWriteNothing(t *testing.T) {
	l := stubQueueLedger(t)
	stubOperatorTerminal(t, false)
	if err := runQueueHold(queueHoldParams{Reason: "x"}); err == nil || !strings.Contains(err.Error(), "terminals") {
		t.Errorf("hold err = %v", err)
	}
	if err := runQueueRelease(); err == nil || !strings.Contains(err.Error(), "terminals") {
		t.Errorf("release err = %v", err)
	}
	if held, _ := l.Hold(); held {
		t.Error("a refused hold took effect")
	}
}

func TestQueueHoldNeedsAReason(t *testing.T) {
	stubQueueLedger(t)
	stubOperatorTerminal(t, true)
	if err := runQueueHold(queueHoldParams{Reason: "  "}); err == nil {
		t.Error("an empty reason must be refused")
	}
}

func TestQueueStatusJSONCarriesTheHold(t *testing.T) {
	l := stubQueueLedger(t)
	noBypass(t)
	if err := l.SetHold("why"); err != nil {
		t.Fatal(err)
	}
	out, err := runQueueStatus(queueStatusParams{Format: "json"})
	if err != nil || !strings.Contains(out, `"Held":true`) || !strings.Contains(out, `"HoldReason":"why"`) {
		t.Errorf("json = %q (%v)", out, err)
	}
}
