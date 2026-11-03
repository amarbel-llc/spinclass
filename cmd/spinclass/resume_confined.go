package main

import (
	"fmt"
	"maps"
	"os"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// scopeEnvVar names the unit launch spec clown re-applies on a resume.
const scopeEnvVar = "CLOWN_AGENT_SCOPE"

// nonResumeHarnessFlags start a new or forked conversation (or pick one by
// something other than its id); clown refuses them for a confined session, and
// a once-confined worktree runs only a harness resume.
var nonResumeHarnessFlags = []string{"-c", "--continue", "--fork-session", "--from-pr"}

// defaultConfinedResume is the harness resume a confined session runs when
// its pinned entry has none (spinclass#453): clown as the frontend, the
// provider's resume of the same transcript after the separator, the recorded
// principal in place of the placeholder, and the recorded profile spliced in
// before the separator by the resume path. The unit spec is handed over on
// the same path, so it never runs unconfined. A trusted [session-entry].resume
// is pinned at creation and wins when set; an unconfined session keeps the
// shell fallback, so no default widens for it.
var defaultConfinedResume = []string{"clown", "--", "--resume", principalPlaceholder}

// confinedResumeScope is the sticky-confined rule for `sc resume`
// (spinclass#400): a session launched confined resumes confined, and nothing of
// a once-confined worktree runs unconfined. It returns the unit spec path to
// hand clown as CLOWN_AGENT_SCOPE and the harness resume to run (resumeEntry,
// or defaultConfinedResume when the pin has none), or "" and nil for a session
// never launched confined. An unknowable state directory counts as confined
// (fail closed). The spec is only checked, never rewritten, and there is no
// opt-out.
func confinedResumeScope(stateDir string, stateDirErr error, st *session.State, resumeEntry []string, shellFallback bool) (spec string, resume []string, err error) {
	key := st.SessionKey
	closeIt := closeCost(st)
	if stateDirErr != nil {
		return "", nil, fmt.Errorf("cannot tell whether %s was launched confined (%v), so resuming it could run it unconfined (spinclass#400); %s", key, stateDirErr, closeIt)
	}
	if !confinement.LaunchedConfined(stateDir, st.WorktreePath) {
		return "", nil, nil
	}
	spec = confinement.UnitSpecPath(stateDir, st.WorktreePath)
	if f, err := os.Open(spec); err != nil {
		return "", nil, fmt.Errorf("%s was launched confined, but its unit spec %s is missing or unreadable (%v); resuming would run it unconfined (spinclass#400); %s, or resurrect it as a fresh session", key, spec, err, closeIt)
	} else {
		f.Close()
	}
	if shellFallback {
		return spec, defaultConfinedResume, nil
	}
	for _, a := range resumeEntry {
		for _, bad := range nonResumeHarnessFlags {
			if a == bad || (strings.HasPrefix(bad, "--") && strings.HasPrefix(a, bad+"=")) {
				return "", nil, fmt.Errorf("%s was launched confined, and its resume entry passes %s, which is not a harness resume (spinclass#400); give it one: in ~/.config/spinclass/sweatfile, [session-entry] resume = [\"clown\", \"--\", \"--resume\", \"{principal}\"] (the default when no layer sets it), then `sc resume %s`; or %s", key, a, key, closeIt)
			}
		}
	}
	return spec, resumeEntry, nil
}

// closeCost says what `sc close` of st would discard, counted now: the
// branch's commits not on the default branch and its uncommitted tracked
// changes, and that `sc resurrect` brings back only the commit as of the
// close. The counts are best effort; a git that cannot answer leaves them
// unknown rather than hiding the cost.
func closeCost(st *session.State) string {
	key := st.SessionKey
	commits := "its commits not on the default branch (count unknown)"
	if def, err := git.DefaultBranch(st.RepoPath); err == nil {
		commits = fmt.Sprintf("%d commit(s) not on %s", git.CommitsUnintegrated(st.WorktreePath, def, st.Branch), def)
	}
	dirty := "its uncommitted tracked changes (count unknown)"
	if paths, err := git.DirtyTrackedPaths(st.WorktreePath); err == nil {
		dirty = fmt.Sprintf("%d uncommitted tracked change(s)", len(paths))
	}
	return fmt.Sprintf("close it with `sc close %s`, which removes the worktree and its branch, discarding %s and %s (`sc resurrect %s` brings back the commit as of the close, not uncommitted edits)", key, commits, dirty, key)
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
