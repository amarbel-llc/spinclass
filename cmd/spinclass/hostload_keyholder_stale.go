package main

import (
	"fmt"
	"os"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// A worker is spawned by the `spinclass serve` of the session that asks, so
// whether it gets a pair is decided by that serve's BINARY. A serve that started
// before the key holder was deployed keeps its old binary: with the switch on it
// still spawns workers on the operator's agent, and it ignores the [keyholder]
// table silently. host-load names those serves, by the same scan the host-queue
// bypass uses, only while the switch is on (docs/plans/2026-11-25-key-holder.md,
// "Deploy order", step 5).

// keyholderSwitchOnFn says whether the key holder switch is on for the driver's
// own context. A seam for tests.
var keyholderSwitchOnFn = func() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	sw, err := resolveKeyholderSwitch(home, keyholderDriverRepoFn(home))
	return err == nil && sw.On
}

// keyholderStaleServes lists the live serves whose binary predates the key holder,
// or nothing while the switch is off (a host that never turned it on has nothing
// to restart).
func keyholderStaleServes() []QueueBypass {
	if !keyholderSwitchOnFn() {
		return nil
	}
	return serveBypasses(keyholder.BuildMarker)
}

// formatKeyholderStaleServes renders the warning section; empty when there is none.
func formatKeyholderStaleServes(b []QueueBypass) string {
	if len(b) == 0 {
		return ""
	}
	var s strings.Builder
	fmt.Fprintf(&s, "\nKEY HOLDER STALE SERVE: %d live session(s) run a spinclass serve that predates the key holder, so the workers it spawns get no holder and keep the operator's agent; restart them", len(b))
	for _, x := range b {
		name := x.Session
		if name == "" {
			name = "?"
		}
		fmt.Fprintf(&s, "\n  %s (pid %d) %s", name, x.PID, x.Exe)
	}
	return s.String()
}
