package job

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestWriteReadRoundTrip(t *testing.T) {
	wt := t.TempDir()
	end := time.Now().Add(2 * time.Second)
	want := &Job{
		ID:          "merge-123",
		Kind:        KindMerge,
		Status:      StatusSucceeded,
		GitSync:     true,
		ServePID:    os.Getpid(),
		StartedAt:   time.Now(),
		EndedAt:     &end,
		ResultText:  "ok 1 - pre-merge hook\n1..1",
		ResultIsErr: false,
	}
	if err := Write(wt, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(wt)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.ID != want.ID || got.Kind != want.Kind || got.Status != want.Status ||
		got.GitSync != want.GitSync || got.ResultText != want.ResultText {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
}

func TestReadNotExist(t *testing.T) {
	wt := t.TempDir()
	if _, err := Read(wt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want os.ErrNotExist, got %v", err)
	}
}

func TestReadRunningWithDeadServeIsInterrupted(t *testing.T) {
	wt := t.TempDir()
	// A reliably-dead pid: run a process to completion, reuse its pid.
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatalf("seed process: %v", err)
	}
	deadPID := c.Process.Pid

	if err := Write(wt, &Job{
		ID: "x", Kind: KindCheck, Status: StatusRunning,
		ServePID: deadPID, StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(wt)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Status != StatusInterrupted {
		t.Fatalf("want %s for dead serve pid, got %s", StatusInterrupted, got.Status)
	}
}

func TestReadRunningWithLiveServeStaysRunning(t *testing.T) {
	wt := t.TempDir()
	if err := Write(wt, &Job{
		ID: "x", Kind: KindCheck, Status: StatusRunning,
		ServePID: os.Getpid(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(wt)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Status != StatusRunning {
		t.Fatalf("want running for live serve pid, got %s", got.Status)
	}
}

func TestStartRefusesSecondJob(t *testing.T) {
	wt := t.TempDir()
	started := make(chan struct{})
	block := func(ctx context.Context, w io.Writer) (string, bool) {
		close(started)
		<-ctx.Done()
		return "", true
	}
	if _, err := Start(wt, KindCheck, false, "first", block); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	<-started
	if !IsRunning(wt) {
		t.Fatal("expected job to be running")
	}
	if _, err := Start(wt, KindCheck, false, "second", block); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("want ErrAlreadyRunning, got %v", err)
	}
	Cancel(wt)
	waitStatus(t, wt, StatusAborted)
}

func TestStartSucceeds(t *testing.T) {
	wt := t.TempDir()
	fn := func(ctx context.Context, w io.Writer) (string, bool) {
		_, _ = io.WriteString(w, "hello from hook\n")
		return "ok 1 - done", false
	}
	if _, err := Start(wt, KindMerge, true, "ok-job", fn); err != nil {
		t.Fatalf("Start: %v", err)
	}
	j := waitStatus(t, wt, StatusSucceeded)
	if j.ResultText != "ok 1 - done" {
		t.Fatalf("result text: %q", j.ResultText)
	}
	if got := TailLog(wt, 5); got != "hello from hook" {
		t.Fatalf("tail log: %q", got)
	}
}

func TestWaitDoneNoJobIsClosed(t *testing.T) {
	wt := t.TempDir()
	select {
	case <-WaitDone(wt):
	default:
		t.Fatal("expected an already-closed channel when no job is running")
	}
}

func TestWaitDoneClosesAfterTerminalRecord(t *testing.T) {
	wt := t.TempDir()
	release := make(chan struct{})
	fn := func(ctx context.Context, w io.Writer) (string, bool) {
		<-release
		return "ok 1 - done", false
	}
	if _, err := Start(wt, KindCheck, false, "wait-job", fn); err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := WaitDone(wt)
	select {
	case <-done:
		t.Fatal("WaitDone closed while the job was still running")
	default:
	}

	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitDone did not close after the job finished")
	}

	// The terminal record is readable the moment done closes.
	j, err := Read(wt)
	if err != nil || j.Status != StatusSucceeded {
		t.Fatalf("expected succeeded after WaitDone closed, got status=%v err=%v", j.Status, err)
	}
}

// TestWriteIsAtomicUnderConcurrentReads: session-job-status reads job.json
// while the job goroutine rewrites it (running -> clown-id -> terminal). A
// non-atomic write lets a reader observe a truncated file ("unexpected end
// of JSON input" — seen in CI). Hammer concurrent writes/reads: every read
// must yield either a complete record or (transiently, pre-first-write
// only) not-exist — never a parse error.
func TestWriteIsAtomicUnderConcurrentReads(t *testing.T) {
	wt := t.TempDir()
	j := &Job{ID: "atomic", Kind: KindCheck, Status: StatusRunning, ServePID: os.Getpid(), StartedAt: time.Now()}
	if err := Write(wt, j); err != nil {
		t.Fatalf("seed Write: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			// Alternate growing/shrinking payloads so a torn read is
			// detectable regardless of which write it interleaves with.
			j.ResultText = strings.Repeat("x", (i%7)*512)
			if err := Write(wt, j); err != nil {
				t.Errorf("Write: %v", err)
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		default:
		}
		if _, err := Read(wt); err != nil {
			t.Fatalf("concurrent Read failed (torn write?): %v", err)
		}
	}
}

// waitStatus polls Read until the job reaches want or the deadline passes.
func waitStatus(t *testing.T, wt, want string) *Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := Read(wt)
		if err == nil && j.Status == want {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job did not reach status %q in time", want)
	return nil
}
