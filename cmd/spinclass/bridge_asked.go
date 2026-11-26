package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// bridgeAskedRead is what a session was launched to have and what clown said
// of it: asked is the stored launch spec's `bridge` ("offered", "required" or
// ""), result is the `bridge` field of clown's result (the unit record):
// "recorded", "off: <reason>" or "" (absent, or no readable record). Both come
// from files in the frontend's state directory, never from the worktree. A seam
// for tests.
var bridgeAskedRead = func(st *session.State) (asked, result string) {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil || st == nil || st.WorktreePath == "" {
		return "", ""
	}
	var spec struct {
		Bridge string `json:"bridge"`
	}
	if b, err := os.ReadFile(confinement.UnitSpecPath(stateDir, st.WorktreePath)); err == nil {
		_ = json.Unmarshal(b, &spec)
	}
	if spec.Bridge != "offered" && spec.Bridge != "required" {
		return "", ""
	}
	b, err := os.ReadFile(confinement.UnitRecordPath(stateDir, st.WorktreePath))
	if err != nil {
		return spec.Bridge, ""
	}
	rec, err := confinement.ParseUnitRecord(b)
	if err != nil {
		return spec.Bridge, ""
	}
	return spec.Bridge, rec.Bridge
}

// bridgeAskedNote says what neither the state file nor clown's own screen says
// to the operator: a live session that was launched to have a bridge (the spec
// asked for it, `offered` or `required`) and has none. It speaks of a result
// that says `off: <reason>`, and of a result that says `recorded` while the
// serve has written no state at all. A session that is not running has no
// bridge to speak of, one whose spec did not ask has nothing to report, and a
// clown that predates the result field (no word either way) is not accused: a
// bridge that is down is the state file's to say. Text from clown's result is
// bounded to the safe-reason alphabet.
func bridgeAskedNote(st *session.State, stateHome string) string {
	if st == nil || st.ResolveState() != session.StateActive {
		return ""
	}
	asked, result := bridgeAskedRead(st)
	if asked == "" {
		return ""
	}
	switch {
	case result == "off" || strings.HasPrefix(result, "off:"):
		reason := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(result, "off"), ":"))
		if reason == "" {
			reason = "clown gave no reason"
		}
		return "bridge: asked for (" + asked + "), not running: " + crossinglog.SafeReason(reason)
	case result == "recorded" && stateHome != "" && st.Principal != "":
		if _, ok := crossinglog.ReadState(crossinglog.StateDir(stateHome), st.Principal); !ok {
			return "bridge: asked for (" + asked + "), not running: the serve has reported no bridge state"
		}
	}
	return ""
}

// defaultBridgeStateHome is the XDG state base `sc list` reads the bridge
// files from.
func defaultBridgeStateHome() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(stateHome) {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return stateHome
}

// bridgeSessionNote is the whole bridge line for one session in session-health
// (withListening) or `sc list`: the serve's own report, then the launch that
// asked for a bridge it did not get.
func bridgeSessionNote(st *session.State, stateHome string, withListening bool) string {
	if st == nil {
		return ""
	}
	return joinNotes(bridgeStateNote(stateHome, st.Principal, withListening), bridgeAskedNote(st, stateHome))
}
