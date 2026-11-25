package main

import (
	"context"
	"fmt"

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
	// bridgeListenerFailed is what runs when the accept loop dies on its own.
	bridgeListenerFailed = handleBridgeListenerFailure
)

// superviseBridge serves s until ctx ends. An accept loop that dies on its own
// (Serve returns an error while ctx is still live) is reported through
// bridgeListenerFailed; a stop through ctx or Close is not.
func superviseBridge(ctx context.Context, s bridgeServing, sessionKey string) {
	if err := s.Serve(ctx); err != nil && ctx.Err() == nil {
		bridgeListenerFailed(sessionKey, err)
	}
}

// handleBridgeListenerFailure is the serve process's answer to a dead bridge
// listener: one log line, a spinclass.bridge.listener_error count, and a wake
// to the session's holders through the restart-wake path (the requester slot
// is empty: nobody asked). The session record is read from the frontend's
// store, which the serve process can reach; with no record the failure is
// still logged and counted and nobody is woken.
func handleBridgeListenerFailure(sessionKey string, err error) {
	sf, loaded := mergedSweatfileForCwd()
	permissionStatsdClient(sf, loaded).Count("bridge.listener_error")

	st, findErr := bridgeFindSession(sessionKey)
	if findErr != nil || st == nil {
		bridgeLogf("serve.bridge listener died session=%s err=%v; no session record to wake holders from (%v)", sessionKey, err, findErr)
		return
	}
	targets := st.RestartWakeTargets("", currentPrincipal())
	bridgeLogf("serve.bridge listener died session=%s err=%v; waking %d holders", sessionKey, err, len(targets))
	msg := fmt.Sprintf("the bridge listener of %s died (%v); its shim cannot reach the frontend until the session restarts", sessionKey, err)
	wakeRestartAudience(st, "bridge-listener", targets, msg)
}
