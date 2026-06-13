package session

import "strings"

// inheritedSessionIDVars are session-identity environment variables that a
// parent (driver) process must NOT leak into a child session it launches. A
// spawned worker or a nested-launched session inherits the parent's process
// environment via os.Environ(); if CLOWN_SESSION_ID rides along, clown's
// ensureJobWakeupEnv (set-only-if-unset, RFC-0009 §2) defers to the leaked
// parent value, so the child's job-watch monitor arms the PARENT's channel
// instead of its own. Directed chat wakes to the child's own key are then
// silently dropped (spinclass#169). CLAUDE_SESSION_ID is stripped for the same
// hygiene (the child is its own Claude session).
//
// spinclass must never SET CLOWN_SESSION_ID itself — clown owns its derivation.
// This helper only stops PROPAGATING a parent's value, so the child re-derives
// its channel from its own (authoritative) SPINCLASS_SESSION_ID.
var inheritedSessionIDVars = []string{
	"CLOWN_SESSION_ID=",
	"CLAUDE_SESSION_ID=",
}

// StripInheritedSessionIDs returns env with any inherited session-identity
// variables removed, so a child session launched with the result re-derives
// its own clown channel rather than inheriting the launcher's. Pass the
// launcher's os.Environ(); the spinclass-owned SPINCLASS_* vars (appended or
// os.Setenv'd separately by callers) are untouched. See spinclass#169.
func StripInheritedSessionIDs(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		drop := false
		for _, v := range inheritedSessionIDVars {
			if strings.HasPrefix(e, v) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, e)
		}
	}
	return out
}
