package main

import (
	"context"
	"os"
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

func TestGateSignalContextRestoresDefaultAfterFirstSignal(t *testing.T) {
	ctx, stop := gateSignalContext(context.Background(), serveSignals...)
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	awaitDone(t, ctx)

	deadline := time.Now().Add(2 * time.Second)
	for signal.Ignored(syscall.SIGTERM) {
		if time.Now().After(deadline) {
			t.Fatal("SIGTERM still ignored after first signal")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The helper's registration was released, not leaked: a fresh
	// registration still receives the signal.
	fresh, freshStop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer freshStop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	awaitDone(t, fresh)
}
