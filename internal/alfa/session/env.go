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
//
// The parent's troupe credentials are stripped too: a child that inherits
// TROUPE_XMPP_PASSWORD_FILE keeps the PARENT's password path next to its own
// user wherever clown does not re-mint it, and the same uid can read that
// file, so the child could authenticate as its parent. The child's clown
// mints its own (spinclass#169, FDR 0035).
//
// CLOWN_UNIT marks a process inside a confined unit. A child session is not
// in its launcher's unit (a confined one gets its own, and its own marker,
// from clown), and an inherited marker would make the child's hook defer grit
// calls it should ask about. Dropping it fails closed: an unmarked hook asks
// (FDR 0035 "CLOWN_UNIT").
//
// The profile pin variables (clown FDR 0022, #407) are per-launch too:
// CLOWN_PROFILE_DIGEST is an approval and CLOWN_PROFILE_SESSION binds it to one
// session, and clown keeps CLOWN_PROFILE in its environment, so an inherited
// one would apply the parent's profile to a child that asked for none. The
// launcher sets a child's own pin explicitly (spawn.ProfileLaunchEnv).
//
// The bridge variables name one session's bridge: its directory, its unit and
// its cgroup file. A child that inherited them would listen on, or be told it
// serves, its launcher's bridge; clown sets them for the child's own launch
// (empty when the bridge is off), so nothing may ride along.
var inheritedSessionIDVars = []string{
	"CLOWN_SESSION_ID=",
	"CLAUDE_SESSION_ID=",
	"CLOWN_UNIT=",
	"CLOWN_BRIDGE_DIR=",
	"CLOWN_BRIDGE_UNIT=",
	"CLOWN_BRIDGE_CGROUP_FILE=",
	"CLOWN_BRIDGE_SOCKET=",
	"TROUPE_XMPP_PASSWORD_FILE=",
	"TROUPE_XMPP_PASSWORD=",
	"CLOWN_PROFILE=",
	"CLOWN_PROFILE_DIGEST=",
	"CLOWN_PROFILE_SESSION=",
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
