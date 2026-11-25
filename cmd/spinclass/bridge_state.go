package main

import (
	"os"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/purse-first/libs/dewey/pkgs/mesa"

	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
)

// bridgeStateNote reads a session's bridge state file under stateHome (the XDG
// state base) and says what is worth saying about it: "bridge DOWN: <reason>"
// for a listener that is not up, and, when withListening is set, "bridge
// listening" for one that is. A session with no file, or one that is not in the
// shape the serve writes, has nothing to say.
func bridgeStateNote(stateHome, principal string, withListening bool) string {
	if stateHome == "" || principal == "" {
		return ""
	}
	st, ok := crossinglog.ReadState(crossinglog.StateDir(stateHome), principal)
	if !ok {
		return ""
	}
	if n := st.Note(); n != "" {
		return n
	}
	if withListening {
		return "bridge listening"
	}
	return ""
}

// listBridgeNote is what `sc list` says about a session's bridge: only a
// bridge that is down. A seam for tests.
var listBridgeNote = func(principal string) string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(stateHome) {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return bridgeStateNote(stateHome, principal, false)
}

// serveBridgeCell is the plain table's SERVE cell with the session's bridge
// note after the build verdict: a bridge that is down is as worth seeing as a
// serve that runs old code.
func serveBridgeCell(builds []string, own, bridge string) mesa.Cell {
	if bridge == "" {
		return serveCell(builds, own)
	}
	text, _ := serveVerdict(builds, own, false)
	return mesa.Styled(mesa.Warn, joinNotes(text, bridge))
}

// serveBridgeNote is the TTY continuation row under STATUS: the serve verdict
// when it is worth a line, then the bridge note.
func serveBridgeNote(builds []string, own, bridge string) *mesa.Cell {
	if bridge == "" {
		return serveNote(builds, own)
	}
	text, _ := serveVerdict(builds, own, true)
	if text == "current" {
		text = ""
	}
	c := mesa.Styled(mesa.Warn, joinNotes(text, bridge))
	return &c
}

func joinNotes(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "; ")
}
