package main

import (
	"fmt"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// A worker is spawned by the `spinclass serve` of the session that asks, so
// whether it gets a pair is decided by that serve's BINARY. A serve that started
// before the key holder was deployed keeps its old binary: once the switch is on
// it still spawns workers on the operator's agent, and it ignores the [keyholder]
// table silently. host-load names those serves, by the same scan the host-queue
// bypass uses.
//
// The list is a fact about binaries, not about the switch: it is shown whether the
// switch is on or off, so the operator can check it BEFORE turning the switch on
// (docs/plans/2026-11-25-key-holder.md, "Deploy order", step 5: restart until the
// list is empty, then step 6). It depends on no sweatfile, so it answers the same
// from any working directory.

// keyholderStaleMarker is the string a binary that carries the key holder holds. A
// seam for tests.
var keyholderStaleMarker = keyholder.BuildMarker

// keyholderStaleServes lists the live serves whose binary predates the key holder.
func keyholderStaleServes() []QueueBypass {
	return serveBypasses(keyholderStaleMarker)
}

// formatKeyholderStaleServes renders the warning section; empty when there is none.
func formatKeyholderStaleServes(b []QueueBypass) string {
	if len(b) == 0 {
		return ""
	}
	var s strings.Builder
	fmt.Fprintf(&s, "\nKEY HOLDER STALE SERVE: %d live session(s) run a spinclass serve that predates the key holder: once the key holder is switched on, the workers such a serve spawns get no holder and keep the operator's agent; restart them before switching it on (an operator step: with sc restart or restart-child-session, or close and respawn; nothing to do while the key holder stays off)", len(b))
	for _, x := range b {
		name := x.Session
		if name == "" {
			name = "?"
		}
		fmt.Fprintf(&s, "\n  %s (pid %d) %s", name, x.PID, x.Exe)
	}
	return s.String()
}
