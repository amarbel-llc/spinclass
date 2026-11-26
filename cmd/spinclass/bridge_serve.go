package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code.linenisgreat.com/clown/pkgs/peercheck"
	"code.linenisgreat.com/spinclass/internal/0/hungstatus"
	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
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
// clown's peercheck.CheckPeerExact(conn, wantCgroup): the peer's cgroup must
// equal the unit's, strictly (no descendants), and a peer with no pidfd is
// refused. A nil check keeps the bridge off with a named reason
// (bridgeserver.ErrNoPeerCheck), never a weaker default; tests swap it.
var bridgeCheckPeer func(conn net.Conn, wantCgroup string) error = peercheck.CheckPeerExact

// resolveBridgePlan turns the environment clown exported into a plan. The ONE
// operator switch of the bridge is the launch spec's `bridge` field (set from
// the trusted [confinement] bridge knob when the session was launched); clown
// exports the bridge directory and unit only when that field asked for it and
// sets them to the empty string otherwise. So the env alone decides: both
// variables non-empty means this serve listens. No sweatfile is read here, so
// a repo layer has nothing to turn on and a session pays nothing for the code
// when clown named no bridge.
func resolveBridgePlan(getenv func(string) string) bridgePlan {
	dir, unit, ok := bridgeserver.EnvConfig(getenv)
	if !ok {
		return bridgePlan{Reason: bridgeEnvUnsetReason, Quiet: true}
	}
	return bridgePlan{Dir: dir, Unit: unit, CgroupFile: bridgeserver.EnvCgroupFile(getenv)}
}

// bridgeResolve is the seam tests replace for the environment read behind the
// plan.
var bridgeResolve = func() bridgePlan { return resolveBridgePlan(os.Getenv) }

// bridgeRecordedUnit is the agent unit the frontend recorded for the session
// (clown writes the record once the provider has started) and when the record
// was written; ok is false when there is no readable record. A seam for tests.
var bridgeRecordedUnit = func(sessionKey string) (unit string, written time.Time, ok bool) {
	st, err := session.FindByTarget(sessionKey)
	if err != nil || st == nil || st.IsTombstone() || st.Key() != sessionKey {
		return "", time.Time{}, false
	}
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return "", time.Time{}, false
	}
	rec, err := confinement.LoadUnitRecord(confinement.Host{}, stateDir, st.WorktreePath)
	if err != nil {
		return "", time.Time{}, false
	}
	fi, err := os.Stat(confinement.UnitRecordPath(stateDir, st.WorktreePath))
	if err != nil {
		return "", time.Time{}, false
	}
	return rec.Unit, fi.ModTime(), true
}

// bridgePrincipalMapping is the frontend's principal mapping of a principal. A
// seam for tests.
var bridgePrincipalMapping = func(principal string) (confinement.PrincipalMapping, error) {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return confinement.PrincipalMapping{}, err
	}
	return confinement.LoadPrincipalMapping(confinement.Host{}, stateDir, principal)
}

// bridgeReadiness is the listener's Ready for the session service. The
// principal mapping and the unit record exist only after the unit is up (the
// spawn writes the mapping after the launch; clown writes the record once the
// provider has started), so neither can be a condition of starting, and neither
// gates control. Until they hold a verb call is answered ERR refused:
//
//   - no mapping yet, or one that cannot be used: not ready;
//   - a mapping that names ANOTHER session: hard failure at any time;
//   - a record written before this serve started is the previous launch's and
//     is ignored; no record at all is fine (the cgroup file admits a peer);
//   - a record written after this serve started that names another unit than
//     the one clown exported: hard failure.
func bridgeReadiness(key, principal, unit string, serveStart time.Time) func() error {
	return func() error {
		m, err := bridgePrincipalMapping(principal)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return &bridgeserver.NotReadyError{Reason: "the frontend has not written this session's principal mapping yet"}
		case err != nil:
			return &bridgeserver.NotReadyError{Reason: "the frontend's principal mapping for this session cannot be used yet"}
		case m.SessionKey != key:
			return fmt.Errorf("the principal's mapping names %s, not this listener's session %s", m.SessionKey, key)
		}
		if recorded, written, ok := bridgeRecordedUnit(key); ok && written.After(serveStart) && recorded != unit {
			return fmt.Errorf("the unit clown named for the bridge (%s) is not the unit recorded for this session (%s)", unit, recorded)
		}
		return nil
	}
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
	// Bind at once. Every bridged call acts on the session this listener
	// carries, so the frontend's principal mapping must name this key, and the
	// unit clown named must not contradict a record written after this serve
	// started; both are checked per verb call (bridgeReadiness), not here:
	// neither exists yet when clown starts serve and waits for the socket.
	serveStart := time.Now()
	log := openBridgeCrossingLog()
	// The statsd endpoint resolves once, from the sweatfile's [metrics] and
	// the environment, as the permission counters do.
	sf, loaded := mergedSweatfileForCwd()
	stats := permissionStatsdClient(sf, loaded)
	stopListener := startBridgeFrom(ctx, plan, bridgeserver.Options{
		SessionKey: key,
		CheckPeer:  bridgeCheckPeer,
		CgroupFile: plan.CgroupFile,
		Ready:      bridgeReadiness(key, currentPrincipal(), plan.Unit, serveStart),
		Handler:    newHandler(log, stats),
		Observer:   &bridgeObserver{log: log, stats: stats, signal: bridgeWakeSignal},
		Logf:       servelog.Errorf,
	})
	if stopListener == nil {
		// startBridgeFrom reported the failed start and marked the state down.
		clearState()
		return nil
	}
	stopBeat := func() {}
	if w != nil {
		w.mark(true, "")
		stopBeat = startBridgeHeartbeat(ctx, w)
	}
	return func() {
		stopBeat() // before the file goes, so a late beat cannot bring it back
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
	pid := os.Getpid()
	ticks, _ := hungstatus.StartTicks(bridgeProcRoot, pid)
	return &bridgeStateWriter{dir: crossinglog.StateDir(stateDir), principal: currentPrincipal(), key: key, pid: pid, ticks: ticks}
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
