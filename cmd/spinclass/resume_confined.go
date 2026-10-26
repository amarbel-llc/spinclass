package main

import (
	"fmt"
	"maps"
	"os"
	"strings"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// scopeEnvVar names the unit launch spec clown re-applies on a resume.
const scopeEnvVar = "CLOWN_AGENT_SCOPE"

// nonResumeHarnessFlags start a new or forked conversation (or pick one by
// something other than its id); clown refuses them for a confined session, and
// a once-confined worktree runs only a harness resume.
var nonResumeHarnessFlags = []string{"-c", "--continue", "--fork-session", "--from-pr"}

// confinedResumeScope is the sticky-confined rule for `sc resume`
// (spinclass#400): a session launched confined resumes confined, and nothing of
// a once-confined worktree runs unconfined. It returns the unit spec path to
// hand clown as CLOWN_AGENT_SCOPE, or "" for a session never launched confined.
// An unknowable state directory counts as confined (fail closed). The spec is
// only checked, never rewritten, and there is no opt-out.
func confinedResumeScope(stateDir string, stateDirErr error, st *session.State, resumeEntry []string, shellFallback bool) (string, error) {
	key := st.SessionKey
	closeIt := fmt.Sprintf("close it with `sc close %s`", key)
	if stateDirErr != nil {
		return "", fmt.Errorf("cannot tell whether %s was launched confined (%v), so resuming it could run it unconfined (spinclass#400); %s", key, stateDirErr, closeIt)
	}
	if !confinement.LaunchedConfined(stateDir, st.WorktreePath) {
		return "", nil
	}
	spec := confinement.UnitSpecPath(stateDir, st.WorktreePath)
	if f, err := os.Open(spec); err != nil {
		return "", fmt.Errorf("%s was launched confined, but its unit spec %s is missing or unreadable (%v); resuming would run it unconfined (spinclass#400); %s, or resurrect it as a fresh session", key, spec, err, closeIt)
	} else {
		f.Close()
	}
	wayOut := fmt.Sprintf("resume it with `sc resume %s` once it has a harness resume entry (which relaunches it confined through clown), or %s", key, closeIt)
	if shellFallback {
		return "", fmt.Errorf("%s was launched confined and has no [session-entry].resume, so resuming would open an unconfined shell in a once-confined worktree (spinclass#400); %s", key, wayOut)
	}
	for _, a := range resumeEntry {
		for _, bad := range nonResumeHarnessFlags {
			if a == bad || (strings.HasPrefix(bad, "--") && strings.HasPrefix(a, bad+"=")) {
				return "", fmt.Errorf("%s was launched confined, and its resume entry passes %s, which is not a harness resume (spinclass#400); %s", key, a, wayOut)
			}
		}
	}
	return spec, nil
}

// withScope returns a copy of env naming spec as the unit spec, replacing
// anything configured under the same key. env itself (the session's pinned
// entry) is never modified.
func withScope(env map[string]string, spec string) map[string]string {
	out := maps.Clone(env)
	if out == nil {
		out = map[string]string{}
	}
	out[scopeEnvVar] = spec
	return out
}
