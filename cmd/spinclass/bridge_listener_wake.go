package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/servelog"
)

// bridgeServing is the part of the listener the supervisor runs.
type bridgeServing interface {
	Serve(ctx context.Context) error
}

var (
	// bridgeFindSession resolves the session record whose holders are woken.
	bridgeFindSession = func(key string) (*session.State, error) { return session.FindByTarget(key) }
	// bridgeLogf is the serve log.
	bridgeLogf = servelog.Errorf
	// bridgeListenerFailed is what runs when the listener is dead, whether it
	// died while serving or never came up.
	bridgeListenerFailed = handleBridgeListenerFailure
	// bridgeWakeInterval is the least time between two wakes of one kind for one
	// session: a listener that fails every start must not page its holders on
	// every serve start.
	bridgeWakeInterval = 10 * time.Minute
	// bridgeNow is the clock of the wake limiter.
	bridgeNow = time.Now
	// bridgeWakes is the limiter's memory.
	bridgeWakes = newWakeLimiter()
	// bridgeStateOut is this serve's state-file writer, set while a bridge plan
	// is live and nil otherwise (nothing is written then).
	bridgeStateOut atomic.Pointer[bridgeStateWriter]
)

// wakeLimiter lets one wake per (session, kind) through per interval.
type wakeLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newWakeLimiter() *wakeLimiter { return &wakeLimiter{last: map[string]time.Time{}} }

func (l *wakeLimiter) allow(key, kind string, now time.Time, every time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := key + "\x00" + kind
	if t, ok := l.last[k]; ok && now.Sub(t) < every {
		return false
	}
	l.last[k] = now
	return true
}

// bridgeStateWriter keeps the bridge state file of one session's listener.
type bridgeStateWriter struct {
	dir, principal, key string
	mu                  sync.Mutex
	last                *crossinglog.State
}

// mark records that the listener is up (reason ignored) or down for a reason.
// Since moves only when the up/down answer changes. A failed write is logged:
// the file is a convenience for the operator, never a reason to fail serve.
func (w *bridgeStateWriter) mark(listening bool, reason string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := bridgeNow().UTC()
	since := now
	if w.last != nil && w.last.Listening == listening {
		since = w.last.Since
	}
	st := crossinglog.State{Session: w.key, Listening: listening, Reason: reason, Since: since, UpdatedAt: now}
	if err := crossinglog.WriteState(w.dir, w.principal, st); err != nil {
		bridgeLogf("serve.bridge state file not written: %v", err)
	}
	w.last = &st
}

// remove deletes the state file: a serve that stopped cleanly has nothing to
// report.
func (w *bridgeStateWriter) remove() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.Remove(crossinglog.StatePath(w.dir, w.principal)); err != nil && !errors.Is(err, os.ErrNotExist) {
		bridgeLogf("serve.bridge state file not removed: %v", err)
	}
	w.last = nil
}

// superviseBridge serves s until ctx ends. An accept loop that dies on its own
// (Serve returns an error while ctx is still live) is reported through
// bridgeListenerFailed; a stop through ctx or Close is not.
func superviseBridge(ctx context.Context, s bridgeServing, sessionKey string) {
	if err := s.Serve(ctx); err != nil && ctx.Err() == nil {
		bridgeListenerFailed(sessionKey, err)
	}
}

// handleBridgeListenerFailure is the serve process's answer to a dead bridge
// listener (one that died while serving or never came up): one log line, a
// spinclass.bridge.listener_error count, the state file marked down, and a wake
// to the session's holders through the restart-wake path (the requester slot
// is empty: nobody asked), at most once per interval. The session record is
// read from the frontend's store, which the serve process can reach; with no
// record the failure is still logged, counted and marked, and nobody is woken.
func handleBridgeListenerFailure(sessionKey string, err error) {
	sf, loaded := mergedSweatfileForCwd()
	permissionStatsdClient(sf, loaded).Count("bridge.listener_error")
	if w := bridgeStateOut.Load(); w != nil {
		w.mark(false, err.Error())
	}
	msg := fmt.Sprintf("the bridge listener of %s died (%v); its shim cannot reach the frontend until the session restarts", sessionKey, err)
	bridgeWake(sessionKey, "listener", fmt.Sprintf("listener died err=%v", err), msg)
}

// bridgeWake tells a session's other holders (or, when it has none, the
// principal that spawned it) that its bridge needs attention, at most once per
// (session, kind) per interval, and logs exactly one line either way. what is
// the log line's account of the event; msg is the wake text.
func bridgeWake(sessionKey, kind, what, msg string) {
	if !bridgeWakes.allow(sessionKey, kind, bridgeNow(), bridgeWakeInterval) {
		bridgeLogf("serve.bridge %s session=%s: %s; wake suppressed (one per %v)", kind, sessionKey, what, bridgeWakeInterval)
		return
	}
	st, findErr := bridgeFindSession(sessionKey)
	if findErr != nil || st == nil {
		bridgeLogf("serve.bridge %s session=%s: %s; no session record to wake holders from (%v)", kind, sessionKey, what, findErr)
		return
	}
	own := currentPrincipal()
	targets := st.RestartWakeTargets("", own)
	if len(targets) == 0 && st.SpawnedByPrincipal != "" && st.SpawnedByPrincipal != own {
		// Nobody else holds a handle: the principal that spawned the session is
		// the one that can still act on it.
		targets = []string{st.SpawnedByPrincipal}
	}
	bridgeLogf("serve.bridge %s session=%s: %s; waking %d holders", kind, sessionKey, what, len(targets))
	wakeRestartAudience(st, "bridge-"+kind, targets, msg)
}
