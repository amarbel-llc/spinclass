package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"syscall"
	"testing"
	"time"
)

func awaitDone(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("ctx not cancelled by signal within 5s")
	}
}

func TestGateSignalContextCancelsOnSIGTERM(t *testing.T) {
	ctx, stop := gateSignalContext(context.Background(), serveSignals...)
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	awaitDone(t, ctx)
}

func TestGateSignalContextCancelsOnSIGINT(t *testing.T) {
	ctx, stop := gateSignalContext(context.Background(), serveSignals...)
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	awaitDone(t, ctx)
}

func TestGateSignalContextCancelsOnSIGHUP(t *testing.T) {
	ctx, stop := gateSignalContext(context.Background(), cliGateSignals...)
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	awaitDone(t, ctx)
}

func TestServeSignalsDoNotIncludeSIGHUP(t *testing.T) {
	if slices.Contains(serveSignals, os.Signal(syscall.SIGHUP)) {
		t.Error("serveSignals must not include SIGHUP: merges are session-durable")
	}
	for _, want := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		if !slices.Contains(serveSignals, want) {
			t.Errorf("serveSignals missing %v", want)
		}
	}
}

func TestCLIGateSignalsIncludeSIGHUP(t *testing.T) {
	for _, want := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !slices.Contains(cliGateSignals, want) {
			t.Errorf("cliGateSignals missing %v", want)
		}
	}
}

func TestDiscardSIGHUPKeepsProcessAliveAndCtxLive(t *testing.T) {
	ctx, stopCtx := gateSignalContext(context.Background(), serveSignals...)
	defer stopCtx()
	stopDiscard := discardSIGHUP()
	defer stopDiscard()

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	// A follow-up signal we do handle proves the earlier SIGHUP was already
	// delivered and discarded (signals to one process are processed in order
	// by the runtime's single signal loop), without sleeping.
	probe := make(chan os.Signal, 1)
	signal.Notify(probe, syscall.SIGUSR1)
	defer signal.Stop(probe)
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probe:
	case <-time.After(5 * time.Second):
		t.Fatal("probe signal never delivered")
	}
	if ctx.Err() != nil {
		t.Errorf("serve ctx cancelled by SIGHUP: %v", ctx.Err())
	}
}

// awaitProbe blocks until the probe signal (delivered after the signal under
// test) arrives: signals to one process are handled in order, so by then the
// earlier one has been delivered or discarded. No sleeping.
func awaitProbe(t *testing.T) {
	t.Helper()
	probe := make(chan os.Signal, 1)
	signal.Notify(probe, syscall.SIGUSR1)
	defer signal.Stop(probe)
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probe:
	case <-time.After(5 * time.Second):
		t.Fatal("probe signal never delivered")
	}
}

// ignoreSIGHUP simulates `nohup sc merge`: SIGHUP is ignored at process start.
func ignoreSIGHUP(t *testing.T) {
	t.Helper()
	signal.Ignore(syscall.SIGHUP)
	t.Cleanup(func() { signal.Reset(syscall.SIGHUP) })
}

// os/signal: Notify on a signal ignored at process start un-ignores it. A
// signal the user chose to ignore (nohup, a background job) must stay ignored.
func TestGateSignalContextLeavesIgnoredSignalsIgnored(t *testing.T) {
	ignoreSIGHUP(t)
	ctx, stop := gateSignalContext(context.Background(), cliGateSignals...)
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	awaitProbe(t)

	if !signal.Ignored(syscall.SIGHUP) {
		t.Error("SIGHUP was un-ignored by the gate's signal registration")
	}
	if ctx.Err() != nil {
		t.Errorf("gate ctx cancelled by an ignored SIGHUP: %v", ctx.Err())
	}
}

func TestGateSignalContextWithOnlyIgnoredSignalsNeverCancels(t *testing.T) {
	ignoreSIGHUP(t)
	// An empty signal list to NotifyContext would mean EVERY signal.
	ctx, stop := gateSignalContext(context.Background(), syscall.SIGHUP)
	awaitProbe(t) // an unrelated signal: would cancel a gate wrongly built over "all signals"
	if ctx.Err() != nil {
		t.Errorf("gate ctx cancelled by an unrelated signal: %v", ctx.Err())
	}
	stop()
	if ctx.Err() == nil {
		t.Error("stop did not cancel the ctx")
	}
}

func TestDiscardSIGHUPLeavesIgnoredSIGHUPIgnored(t *testing.T) {
	ignoreSIGHUP(t)
	stop := discardSIGHUP()
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	awaitProbe(t)
	if !signal.Ignored(syscall.SIGHUP) {
		t.Error("discardSIGHUP un-ignored an already-ignored SIGHUP")
	}
}

const gateHelperEnv = "SPINCLASS_TEST_GATE_HELPER"

// TestGateSignalContextHelperProcess is the child half of
// TestGateSignalContextRestoresDefaultAfterFirstSignal; it is a no-op in a
// normal test run.
func TestGateSignalContextHelperProcess(t *testing.T) {
	if os.Getenv(gateHelperEnv) != "1" {
		t.Skip("helper process for TestGateSignalContextRestoresDefaultAfterFirstSignal")
	}
	ctx, stop := gateSignalContext(context.Background(), serveSignals...)
	defer stop()
	fmt.Println("ready")
	<-ctx.Done()
	fmt.Println("cancelled")
	select {} // only a second signal's default action ends this process
}

func TestGateSignalContextRestoresDefaultAfterFirstSignal(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestGateSignalContextHelperProcess$")
	cmd.Env = append(os.Environ(), gateHelperEnv+"=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill() // no-op once it has exited
		select {
		case <-waited:
		case <-time.After(10 * time.Second):
		}
	})

	lines := bufio.NewScanner(stdout)
	expectLine := func(want string) {
		t.Helper()
		if !lines.Scan() || lines.Text() != want {
			t.Fatalf("helper: want %q, got %q (err %v)", want, lines.Text(), lines.Err())
		}
	}
	expectLine("ready")
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	expectLine("cancelled")

	// The registration is dropped on a separate goroutine once the ctx is done,
	// so a second SIGINT can still be absorbed for an instant: resend until the
	// process dies. Each wait ends on the real event, the exit.
	deadline := time.After(30 * time.Second)
	var waitErr error
resend:
	for {
		_ = cmd.Process.Signal(syscall.SIGINT)
		select {
		case waitErr = <-waited:
			break resend
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatal("helper survived repeated SIGINTs: default disposition not restored")
		}
	}

	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("helper exit: want ExitError, got %v", waitErr)
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Errorf("helper did not die by SIGINT: %v", exitErr)
	}
}
