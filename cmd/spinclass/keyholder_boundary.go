package main

import (
	"fmt"
	"os"
	"strings"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
)

// keyholderWantsPairFn says whether a launch of this kind would get a key holder
// pair NOW: the [keyholder] mode a trusted layer set, and a launch kind the
// policy gives a pair to. It starts nothing and reads only the sweatfiles, so a
// relaunch can be refused before any pair is started for it. Tests replace it.
var keyholderWantsPairFn = keyholderWantsPair

func keyholderWantsPair(home, repoPath string, spawned bool) (bool, error) {
	if !keyholderPolicyFor(classifyKeyholderLaunch(spawned)).StartsPair {
		return false, nil
	}
	sw, err := resolveKeyholderSwitch(home, repoPath)
	if err != nil {
		return false, fmt.Errorf("%w (a session is not relaunched until it is fixed)", err)
	}
	return sw.On, nil
}

// checkRelaunchBoundary refuses to relaunch a confined session whose stored unit
// spec binds a different agent socket than the key holder switch says it should
// now (B-F2 / A-H1, option (a)). clown re-reads the stored spec on every
// relaunch and refuses a changed boundary; the agent socket is part of the
// boundary, so a changed agent is a changed boundary, and the answer is a new
// session: spinclass says so BEFORE clown is called and before a pair is started
// for the relaunch. Nothing is rewritten, in either direction. A session never
// launched confined has no spec to check.
//
// switchOn is the switch as it is now for THIS launch (keyholderWantsPairFn).
func checkRelaunchBoundary(st *session.State, switchOn bool) error {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return fmt.Errorf("cannot tell whether %s was launched confined (%v), so its relaunch could change what its unit binds; %s", st.Key(), err, closeCost(st))
	}
	if !confinement.LaunchedConfined(stateDir, st.WorktreePath) {
		return nil
	}
	if err := spawn.CheckAgentBoundary(stateDir, st.WorktreePath, switchOn); err != nil {
		return fmt.Errorf("%s was launched confined and %w; %s", st.Key(), err, closeCost(st))
	}
	return nil
}

// relaunchBoundaryNow is checkRelaunchBoundary with the switch read now, for the
// paths that relaunch from the stored spec without building a launch
// environment: `sc resume` and the restart preflight. A session never launched
// confined is answered before the sweatfiles are read.
func relaunchBoundaryNow(st *session.State) error {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return fmt.Errorf("cannot tell whether %s was launched confined (%v), so its relaunch could change what its unit binds; %s", st.Key(), err, closeCost(st))
	}
	if !confinement.LaunchedConfined(stateDir, st.WorktreePath) {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving the home directory for the key holder: %w", err)
	}
	on, err := keyholderWantsPairFn(home, st.RepoPath, resumeIsSpawned(st))
	if err != nil {
		return err
	}
	return checkRelaunchBoundary(st, on)
}

// relaunchBoundaryBeforeRestartFn is the seam the restart preflight (and its
// --rearm) calls, before the pair check, so a restart of a confined session
// whose stored spec no longer matches the switch is refused and no pair is
// started for a relaunch that will not happen.
var relaunchBoundaryBeforeRestartFn = relaunchBoundaryNow

// boundaryRestartRefusal is the reason a restart is refused for a changed agent
// boundary.
func boundaryRestartRefusal(err error) string {
	return fmt.Sprintf("%v; nothing was restarted", err)
}

// checkHandoffEnv refuses a start hand-off environment clown could not confine
// the child from. clown confines a started child by its sidecar record, which
// lives under the child's XDG_STATE_HOME, else by CLOWN_AGENT_SCOPE: an
// environment with neither starts a stopped confined session UNCONFINED, and the
// "resuming UNCONFINED" notice is printed only for a named --resume id. So the
// hand-off must carry the state directory the frontend keeps its records under,
// and, for a session launched confined, that session's own unit spec path.
func checkHandoffEnv(env map[string]string, wantScope string) error {
	if v := env["XDG_STATE_HOME"]; v == "" || !strings.HasPrefix(v, "/") {
		return fmt.Errorf("the start hand-off environment has no absolute XDG_STATE_HOME (%q): clown looks the child's sidecar record up under it, and without one a stopped confined session would come back UNCONFINED; nothing was started", v)
	}
	if wantScope != "" && env[scopeEnvVar] != wantScope {
		return fmt.Errorf("the start hand-off environment's %s is %q, not the child's own unit spec %s: clown would start the stopped confined session from another spec or unconfined; nothing was started", scopeEnvVar, env[scopeEnvVar], wantScope)
	}
	return nil
}
