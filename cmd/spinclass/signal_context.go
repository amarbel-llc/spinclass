package main

import (
	"context"
	"os"
	"os/signal"
	"slices"
	"syscall"
)

var (
	// serveSignals cancel in-flight tool calls. NOT SIGHUP: merges are
	// session-durable (see discardSIGHUP).
	serveSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}
	// cliGateSignals are for sc merge|check|run, which own a terminal.
	cliGateSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
)

// gateSignalContext returns a ctx cancelled by the first of sigs, so a running
// pre-merge hook is torn down and its build worktree removed instead of being
// orphaned (spinclass#188). The registration is dropped as soon as the ctx is
// done, restoring the default disposition: a SECOND signal kills the process
// outright rather than waiting on the teardown.
//
// A signal that was ignored at process start (`nohup sc merge`, a background
// job) is left ignored and not registered: os/signal's Notify would un-ignore
// it, turning the user's explicit opt-out into a cancellation.
func gateSignalContext(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
	sigs = slices.DeleteFunc(slices.Clone(sigs), signal.Ignored)
	if len(sigs) == 0 {
		// NotifyContext with no signals means EVERY signal.
		return context.WithCancel(parent)
	}
	ctx, stop := signal.NotifyContext(parent, sigs...)
	context.AfterFunc(ctx, stop)
	return ctx, stop
}

// discardSIGHUP swallows SIGHUP for the life of serve. serve is a stdio child
// of the client; a dropped terminal must neither kill it mid-merge (default
// action: no cleanup, orphaned hook) nor cancel it (merges outlive the client
// session; the stdin EOF that follows drains in-flight calls). signal.Notify to
// a drained channel, NOT signal.Ignore: an ignored disposition is inherited
// across exec and would make every hook ignore SIGHUP. An already-ignored
// SIGHUP (nohup) is left alone: Notify would un-ignore it.
func discardSIGHUP() (stop func()) {
	if signal.Ignored(syscall.SIGHUP) {
		return func() {}
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range ch {
		}
	}()
	return func() {
		signal.Stop(ch)
		close(ch)
		<-drained
	}
}
