package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

type fakeBridgeServer struct{ err error }

func (f fakeBridgeServer) Serve(context.Context) error { return f.err }

func stubBridgeFailureHook(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	orig := bridgeListenerFailed
	bridgeListenerFailed = func(key string, err error) { calls = append(calls, key+"|"+err.Error()) }
	t.Cleanup(func() { bridgeListenerFailed = orig })
	return &calls
}

func TestSuperviseBridgeReportsAnAcceptLoopThatDiedNotACleanStop(t *testing.T) {
	calls := stubBridgeFailureHook(t)
	superviseBridge(context.Background(), fakeBridgeServer{err: nil}, "repo/feat")
	if len(*calls) != 0 {
		t.Fatalf("a clean stop was reported as a failure: %v", *calls)
	}
	superviseBridge(context.Background(), fakeBridgeServer{err: errors.New("bridge accept: boom")}, "repo/feat")
	if len(*calls) != 1 || (*calls)[0] != "repo/feat|bridge accept: boom" {
		t.Fatalf("calls = %v, want one report of the dead accept loop", *calls)
	}
}

func TestSuperviseBridgeNoReportWhenTheContextEndedFirst(t *testing.T) {
	calls := stubBridgeFailureHook(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	superviseBridge(ctx, fakeBridgeServer{err: errors.New("use of closed network connection")}, "repo/feat")
	if len(*calls) != 0 {
		t.Fatalf("an error during shutdown was reported as a crash: %v", *calls)
	}
}

type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) add(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, f)
}

func stubBridgeLog(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	orig, origWakes := bridgeLogf, bridgeWakes
	bridgeLogf = c.add
	bridgeWakes = newWakeLimiter() // each test is its own session's first failure
	t.Cleanup(func() { bridgeLogf, bridgeWakes = orig, origWakes })
	return c
}

func stubBridgeSession(t *testing.T, st *session.State, err error) {
	t.Helper()
	orig := bridgeFindSession
	bridgeFindSession = func(string) (*session.State, error) { return st, err }
	t.Cleanup(func() { bridgeFindSession = orig })
}

func TestBridgeListenerFailureLogsOnceCountsAndWakesTheOtherHolders(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "self-principal")
	drain := queueStatsdSink(t)
	logs := stubBridgeLog(t)
	calls := stubNotifyPrincipal(t)
	stubBridgeSession(t, &session.State{SessionKey: "repo/feat", Holders: []string{"holder-a", "self-principal", "holder-b"}}, nil)

	handleBridgeListenerFailure("repo/feat", errors.New("bridge accept: too many open files"))

	if len(logs.lines) != 1 {
		t.Errorf("logged %d lines, want exactly 1: %v", len(logs.lines), logs.lines)
	}
	got := map[string]string{}
	for _, c := range *calls {
		got[c.target] = c.message
	}
	if len(got) != 2 || got["holder-a"] == "" || got["holder-b"] == "" {
		t.Fatalf("woke %v, want holder-a and holder-b and never the session itself", got)
	}
	for target, msg := range got {
		if !strings.Contains(msg, "repo/feat") || !strings.Contains(msg, "too many open files") {
			t.Errorf("wake to %s lacks the session or the cause: %q", target, msg)
		}
	}
	names := metricNames(metricDatagrams(t, drain, 1))
	if countName(names, "spinclass.bridge.listener_error") != 1 {
		t.Errorf("metrics = %v, want one spinclass.bridge.listener_error", names)
	}
}

func TestBridgeListenerFailureWithNoSessionRecordStillLogsAndCounts(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "self-principal")
	drain := queueStatsdSink(t)
	logs := stubBridgeLog(t)
	calls := stubNotifyPrincipal(t)
	stubBridgeSession(t, nil, errors.New("no such session"))

	handleBridgeListenerFailure("repo/gone", errors.New("bridge accept: boom"))

	if len(logs.lines) != 1 {
		t.Errorf("logged %d lines, want exactly 1: %v", len(logs.lines), logs.lines)
	}
	if len(*calls) != 0 {
		t.Errorf("woke %v with no record to take holders from", *calls)
	}
	if names := metricNames(metricDatagrams(t, drain, 1)); countName(names, "spinclass.bridge.listener_error") != 1 {
		t.Errorf("metrics = %v", names)
	}
}
