package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/0/sessionlog"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// A lost key holder pair costs the session its key: the keys live in fibby's
// memory and a replaced pair mints a new one, so every certificate for the old
// key is void. The operator accepted that ONLY if he is told, so a re-mint is
// announced on stderr, to the session's holders and spawner (rate limited), and
// shown by `sc list` and session-health until a certificate for the new key is
// installed; and every path that relaunches a session checks the pair first.

// keyholderStatusLauncherFn builds a launcher that only READS: it asks systemd
// whether a recorded pair's units run and starts nothing, so it needs no tools.
// Tests replace it.
var keyholderStatusLauncherFn = func() *keyholder.Launcher {
	return &keyholder.Launcher{Base: keyholderStateBaseFn(), Run: keyholder.SystemdRunner{}, CertList: sessionKeyCertList}
}

// keyholderStateBaseFn is where the pairs' state lives (<state>/spinclass/
// keyholder); tests replace it with a short path (unix sockets live under it).
var keyholderStateBaseFn = keyholder.StateBase

// keyholderNow is the clock the notice rate limit reads; tests replace it.
var keyholderNow = func() time.Time { return time.Now().UTC() }

// remintMessage is the wake text: the session, the old and the new fingerprint,
// when, and what to do. held counts the wakes the rate limit held back.
func remintMessage(sessionKey string, rm *keyholder.Remint, held int) string {
	msg := fmt.Sprintf("the key holder of %s was LOST and replaced at %s: its key CHANGED (old %s, new %s). Every certificate for the old key is void: run `sc session-key %s` and have the new key certified again; until then the session cannot use its delegated access.",
		sessionKey, rm.At.UTC().Format(time.RFC3339), rm.OldFingerprint, rm.NewFingerprint, sessionKey)
	if held > 0 {
		msg += fmt.Sprintf(" (%d further re-mints of this session in the last %s were not announced separately.)", held, keyholder.NoticeInterval)
	}
	return msg
}

// announceKeyholderRemint tells the operator, at the launching terminal and in
// the lifecycle log, that sessionKey has a different key now; and wakes the
// session's holders and its spawner, at most once per keyholder.NoticeInterval.
// Best effort: a failed wake is logged and never fails the launch.
func announceKeyholderRemint(l *keyholder.Launcher, sessionKey string, rm *keyholder.Remint, stderr io.Writer) {
	fmt.Fprintf(stderr, "spinclass: the key holder of %s was lost and a NEW key was minted (old %s, new %s): every certificate for the old key is void; run `sc session-key %s` and certify the new key\n",
		sessionKey, rm.OldFingerprint, rm.NewFingerprint, sessionKey)
	sessionlog.Infof("keyholder: %s re-minted: old %s new %s", sessionKey, rm.OldFingerprint, rm.NewFingerprint)
	send, held := l.ClaimNotice(sessionKey, keyholderNow())
	if !send {
		return
	}
	st, err := session.FindByTarget(sessionKey)
	if err != nil {
		return // no record yet (a first launch) or none to wake
	}
	// Best effort: on error ownerPrincipal returns "", and the session's own
	// principal is then simply not excluded from the audience.
	own, _ := ownerPrincipal(st, "", "restart", "")
	wakeRestartAudience(st, "keyholder", st.RestartWakeTargets(st.SpawnedByPrincipal, own), remintMessage(sessionKey, rm, held))
}

// keyholderBeforeRestartFn is the seam the restart preflight (and its --rearm)
// calls before it approves a relaunch. clown relaunches a session without
// `sc resume`, so nothing else checks its pair on that path.
var keyholderBeforeRestartFn = keyholderBeforeRestart

// keyholderBeforeRestart makes sure a session that was given a pair still has
// one, restarting a lost pair (a new key, announced by the same path as a
// resume's) BEFORE the relaunch. A session that never had a pair, or whose
// switch is off now, is left alone; an error means the pair cannot be had and
// the restart must not go ahead on a dead holder socket.
func keyholderBeforeRestart(st *session.State, stderr io.Writer) error {
	if st.Kind == session.KindImplicit || !keyholder.HasState(keyholderStateBaseFn(), st.Key()) {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving the home directory for the key holder: %w", err)
	}
	// An existing live pair is left alone; a lost one is replaced and announced
	// inside keyholderSessionEnvUndo. The environment it returns is the stored
	// one's job (the relaunch reuses the spec and env the session already had).
	_, _, err = keyholderSessionEnvUndo(home, st.RepoPath, st.Key(), stderr)
	return err
}

// keyholderRestartRefusal is the reason a restart is refused when the pair
// cannot be had.
func keyholderRestartRefusal(st *session.State, err error) string {
	return fmt.Sprintf("the key holder pair of %s is gone and could not be started again (%v), so the session would come back on a holder socket nothing listens on; nothing was restarted. Fix the cause, or switch the key holder off with [keyholder] mode = \"off\" in a trusted sweatfile", st.Key(), err)
}

// keyholderNote is the line `sc list` and session-health show about a session's
// holder: "" when there is nothing to say. includeOK says a healthy pair too
// (session-health does; `sc list` stays quiet about sessions that are fine).
// A session that never had a pair costs two file reads and no systemd call.
func keyholderNote(sessionKey string, includeOK bool) string {
	if !keyholder.HasState(keyholderStateBaseFn(), sessionKey) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h, err := keyholderStatusLauncherFn().HealthOf(ctx, sessionKey)
	if err != nil {
		return fmt.Sprintf("keyholder: state unknown (%v)", err)
	}
	if n := h.Note(); n != "" {
		return n
	}
	if includeOK && h.State == "ok" {
		if h.Cert.State == keyholder.CertNone {
			return "keyholder: ok (" + h.Fingerprint + "), no certificate"
		}
		return "keyholder: ok (" + h.Fingerprint + ")"
	}
	return ""
}
