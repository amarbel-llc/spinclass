package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/servelog"
)

// bridgePlan is what decides whether this serve listens as the session's
// bridge frontend. A plan with an empty Dir means no bridge, and Reason says
// why.
type bridgePlan struct {
	Dir        string
	Unit       string
	CgroupFile string
	Reason     string
}

// bridgeCheckPeer is the exact peer check this build wires into the listener:
// clown's peercheck.CheckPeerExact(conn, wantCgroup). It is nil until a clown
// that has it is merged and pinned; a nil check keeps the bridge off with a
// named reason (bridgeserver.ErrNoPeerCheck), never a weaker default.
var bridgeCheckPeer func(conn net.Conn, wantCgroup string) error

// resolveBridgePlan turns the environment clown exported and the trusted
// [bridge] switch into a plan. Both must hold: the env names where and for
// which unit, and a layer above the repo says enable = true. A repo-layer
// [bridge] never counts.
func resolveBridgePlan(getenv func(string) string, home, repoRoot string) bridgePlan {
	dir, unit, ok := bridgeserver.EnvConfig(getenv)
	if !ok {
		return bridgePlan{Reason: "CLOWN_BRIDGE_DIR or CLOWN_BRIDGE_UNIT is unset: clown named no bridge for this session"}
	}
	h, err := sweatfileio.LoadHierarchy(home, repoRoot)
	if err != nil {
		return bridgePlan{Reason: fmt.Sprintf("sweatfile hierarchy: %v", err)}
	}
	enable, untrusted := sweatfileio.TrustedBridgeEnabled(h, home, repoRoot)
	for _, p := range untrusted {
		servelog.Errorf("serve.bridge: [bridge] in %s is ignored: only sweatfiles above the repo may enable the bridge", p)
	}
	if !enable {
		return bridgePlan{Reason: "no trusted [bridge] enable = true"}
	}
	return bridgePlan{Dir: dir, Unit: unit, CgroupFile: bridgeserver.EnvCgroupFile(getenv)}
}

// bridgeResolve is the seam tests replace for the working-directory and home
// reads behind the plan.
var bridgeResolve = func() bridgePlan {
	cwd, err := os.Getwd()
	if err != nil {
		return bridgePlan{Reason: fmt.Sprintf("working directory unresolved: %v", err)}
	}
	repoRoot, err := git.CommonDir(cwd)
	if err != nil {
		return bridgePlan{Reason: fmt.Sprintf("not in a git checkout, so no sweatfile hierarchy to read: %v", err)}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return bridgePlan{Reason: fmt.Sprintf("home directory unresolved: %v", err)}
	}
	return resolveBridgePlan(os.Getenv, home, repoRoot)
}

// startBridge starts the bridge listener when the plan allows it and returns
// what stops it; nil when serve runs without it, with the reason in the serve
// log. sessionHandler serves the session service. It never fails serve.
func startBridge(ctx context.Context, newHandler func(rec crossingRecorder, stats statsd.Client) bridgeserver.SessionHandler) (stop func()) {
	plan := bridgeResolve()
	if plan.Dir == "" {
		servelog.Infof("serve.bridge off: %s", plan.Reason)
		return nil
	}
	key, err := currentSessionKey()
	if err != nil {
		servelog.Errorf("serve.bridge off: session key unresolved: %v", err)
		return nil
	}
	// No exact peer check wired: the listener stays off, loudly. A weaker check
	// would admit the unit's neighbours, so there is no fallback.
	if bridgeCheckPeer == nil {
		servelog.Errorf("serve.bridge off: %v", bridgeserver.ErrNoPeerCheck)
		bridgeListenerFailed(key, bridgeserver.ErrNoPeerCheck)
		return nil
	}
	// Every bridged call acts on the session this listener carries, so the key
	// must be the one the frontend's own principal-to-session resolution gives
	// this serve's principal. The principal resolves to nothing (the
	// SessionStart stamp is not there yet), or to another session: stay off, and
	// count it like a failed start.
	principalKey, perr := confinement.SessionKeyOfPrincipal(currentPrincipal())
	if perr == nil && principalKey != key {
		perr = fmt.Errorf("the serve's principal resolves to %s, not to this listener's session %s", principalKey, key)
	}
	if perr != nil {
		servelog.Errorf("serve.bridge off: %v", perr)
		bridgeListenerFailed(key, perr)
		return nil
	}
	log := openBridgeCrossingLog()
	// The statsd endpoint resolves once, from the sweatfile's [metrics] and
	// the environment, as the permission counters do.
	sf, loaded := mergedSweatfileForCwd()
	stats := permissionStatsdClient(sf, loaded)
	return startBridgeFrom(ctx, plan, bridgeserver.Options{
		SessionKey: key,
		CheckPeer:  bridgeCheckPeer,
		CgroupFile: plan.CgroupFile,
		Handler:    newHandler(log, stats),
		Observer:   &bridgeObserver{log: log, stats: stats},
		Logf:       servelog.Errorf,
	})
}

// openBridgeCrossingLog opens this principal's crossing log under
// <state>/spinclass/bridge. A state directory that cannot be resolved gives a
// nil log, which records nothing; the bridge still starts.
func openBridgeCrossingLog() *crossinglog.Log {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		servelog.Errorf("serve.bridge crossing log off: %v", err)
		return nil
	}
	return crossinglog.Open(crossinglog.Options{
		Dir:       filepath.Join(stateDir, "spinclass", "bridge"),
		Principal: currentPrincipal(),
		Logf:      servelog.Errorf,
	})
}

// startBridgeFrom listens per the plan with opts and serves until the
// returned stop, which also removes the socket. Any failure logs one line and
// returns nil.
func startBridgeFrom(ctx context.Context, plan bridgePlan, opts bridgeserver.Options) (stop func()) {
	if plan.Dir == "" {
		return nil
	}
	s, err := bridgeserver.Listen(plan.Dir, plan.Unit, opts)
	if err != nil {
		servelog.Errorf("serve.bridge off: %v", err)
		return nil
	}
	servelog.Infof("serve.bridge listening path=%s unit=%s session=%s", s.Path(), plan.Unit, opts.SessionKey)
	done := make(chan struct{})
	go func() {
		superviseBridge(ctx, s, opts.SessionKey)
		close(done)
	}()
	return func() {
		_ = s.Close()
		<-done
	}
}
