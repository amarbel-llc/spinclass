package main

import (
	"strings"

	"code.linenisgreat.com/purse-first/libs/dewey/pkgs/mesa"

	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
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
	// A listening state is believed only while the serve that wrote it lives:
	// a serve that was killed cannot say so, and its file stays.
	if n := st.NoteAt(bridgeProcRoot, bridgeNow()); n != "" {
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
	return bridgeStateNote(defaultBridgeStateHome(), principal, false)
}

// listBridgeSessionNote is the bridge words of one `sc list` row: the serve's
// own report of a bridge that is down, then a launch that asked for a bridge
// it does not have. A seam for tests.
var listBridgeSessionNote = func(st *session.State) string {
	return joinNotes(listBridgeNote(st.Principal), bridgeAskedNote(st, defaultBridgeStateHome()))
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
