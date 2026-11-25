package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
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
	// Quiet marks a serve that has no bridge at all (clown named none): it says
	// nothing and does nothing, so a session without a bridge pays nothing for
	// the code.
	Quiet bool
}

// bridgeEnvUnsetReason is the reason of a serve clown gave no bridge.
const bridgeEnvUnsetReason = "CLOWN_BRIDGE_DIR or CLOWN_BRIDGE_UNIT is unset or empty: clown named no bridge for this session"

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
		return bridgePlan{Reason: bridgeEnvUnsetReason, Quiet: true}
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
	// The environment is read first and alone: a serve clown gave no bridge
	// reads no directory, runs no git and writes no line.
	if _, _, ok := bridgeserver.EnvConfig(os.Getenv); !ok {
		return bridgePlan{Reason: bridgeEnvUnsetReason, Quiet: true}
	}
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

// bridgeRecordedUnit is the agent unit the frontend recorded for the session
// (clown writes the record once the unit is active); ok is false when there is
// no readable record. A seam for tests.
var bridgeRecordedUnit = func(sessionKey string) (unit string, ok bool) {
	st, err := session.FindByTarget(sessionKey)
	if err != nil || st == nil || st.IsTombstone() || st.Key() != sessionKey {
		return "", false
	}
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return "", false
	}
	rec, err := confinement.LoadUnitRecord(confinement.Host{}, stateDir, st.WorktreePath)
	if err != nil {
		return "", false
	}
	return rec.Unit, true
}

// startBridge starts the bridge listener when the plan allows it and returns
// what stops it; nil when serve runs without it, with the reason in the serve
// log. sessionHandler serves the session service. It never fails serve.
func startBridge(ctx context.Context, newHandler func(rec crossingRecorder, stats statsd.Client) bridgeserver.SessionHandler) (stop func()) {
	plan := bridgeResolve()
	if plan.Dir == "" {
		if !plan.Quiet {
			servelog.Infof("serve.bridge off: %s", plan.Reason)
		}
		return nil
	}
	// The key is the one spinclass exported into this session, and only that:
	// deriving it from the working directory could materialise an implicit
	// session, a side effect a bridge has no business causing.
	key := strings.TrimSpace(os.Getenv("SPINCLASS_SESSION_ID"))
	if key == "" {
		servelog.Errorf("serve.bridge off: SPINCLASS_SESSION_ID is unset, so there is no session for the bridge to carry")
		return nil
	}
	w := newBridgeStateWriter(key)
	bridgeStateOut.Store(w)
	clearState := func() { bridgeStateOut.CompareAndSwap(w, nil) }
	// fail is every way a bridge that was asked for does not come up: logged
	// once, counted and marked down like a listener that died, then forgotten.
	fail := func(err error) (stop func()) {
		servelog.Errorf("serve.bridge off: %v", err)
		bridgeListenerFailed(key, err)
		clearState()
		return nil
	}
	// No exact peer check wired: the listener stays off, loudly. A weaker check
	// would admit the unit's neighbours, so there is no fallback.
	if bridgeCheckPeer == nil {
		return fail(bridgeserver.ErrNoPeerCheck)
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
		return fail(perr)
	}
	// The unit clown named must be the unit the frontend recorded for this
	// session: a bridge for a unit the record does not name is a bridge for
	// someone else's unit. No readable record is not a mismatch (the cgroup
	// file is what admits a peer); a record that names another unit is.
	if recorded, ok := bridgeRecordedUnit(key); ok && recorded != plan.Unit {
		return fail(fmt.Errorf("the unit clown named for the bridge (%s) is not the unit recorded for this session (%s)", plan.Unit, recorded))
	}
	log := openBridgeCrossingLog()
	// The statsd endpoint resolves once, from the sweatfile's [metrics] and
	// the environment, as the permission counters do.
	sf, loaded := mergedSweatfileForCwd()
	stats := permissionStatsdClient(sf, loaded)
	stopListener := startBridgeFrom(ctx, plan, bridgeserver.Options{
		SessionKey: key,
		CheckPeer:  bridgeCheckPeer,
		CgroupFile: plan.CgroupFile,
		Handler:    newHandler(log, stats),
		Observer:   &bridgeObserver{log: log, stats: stats, signal: bridgeWakeSignal},
		Logf:       servelog.Errorf,
	})
	if stopListener == nil {
		// startBridgeFrom reported the failed start and marked the state down.
		clearState()
		return nil
	}
	if w != nil {
		w.mark(true, "")
	}
	return func() {
		stopListener()
		// A serve that ended cleanly has no bridge to report on: the file goes,
		// so `sc list` does not call a closed session's bridge down.
		if w != nil {
			w.remove()
		}
		clearState()
	}
}

// newBridgeStateWriter is the writer of this serve's state file, or nil when
// the state directory cannot be resolved (no file is then written).
func newBridgeStateWriter(key string) *bridgeStateWriter {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		servelog.Errorf("serve.bridge state file off: %v", err)
		return nil
	}
	return &bridgeStateWriter{dir: crossinglog.StateDir(stateDir), principal: currentPrincipal(), key: key}
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
		// A bridge that was asked for and did not come up is a dead listener to
		// the holders as much as one that died later.
		if opts.SessionKey != "" {
			bridgeListenerFailed(opts.SessionKey, err)
		}
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
