package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// startHandoffExecFn runs the start hand-off. A nil env inherits the caller's
// environment through restartHandoffFn exactly as before (the key holder off, so
// nothing changes and the existing seam stays the one tests stub); a non-nil env
// is the whole environment of the command.
var startHandoffExecFn = func(dir string, argv, env []string) (string, error) {
	if env == nil {
		return restartHandoffFn(dir, argv)
	}
	return runHandoffWithEnv(dir, argv, env)
}

// startHandoffEnvFor is the environment the start hand-off runs the relaunch
// with when the key holder is on (A-M1). The hand-off used to run with the
// CALLER's whole environment: a started child then carried the caller's operator
// agent variables or the caller's holder socket, and the caller's own
// CLOWN_AGENT_SCOPE. A started child is always a worker for the key holder (it is
// a detached relaunch: the operator's terminal is not its terminal), so it gets
// its own pair and the scrub.
//
// A child launched confined is relaunched from its stored unit spec, which clown
// re-reads and refuses when its boundary changed: a spec that does not match the
// switch as it is now (written with the switch off and the switch on now, or
// with the holder and the switch off now) is refused HERE, before a pair is
// started and before clown is called, whatever the switch says (B-F2 / A-H1:
// a changed agent means a new session). The environment it builds must carry the
// state directory clown finds the child's sidecar record under and the child's
// own CLOWN_AGENT_SCOPE, or the stopped confined session would come back
// unconfined: an environment without them is refused (checkHandoffEnv).
//
// It returns nil, nil, nil when the key holder is off and the child was not
// launched confined: the hand-off is then run as it always was.
func startHandoffEnvFor(child *session.State, base []string, stderr io.Writer) ([]string, func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, fmt.Errorf("resolving the home directory for the key holder: %w", err)
	}
	switchOn, err := keyholderWantsPairFn(home, child.RepoPath, true)
	if err != nil {
		return nil, nil, err
	}
	stateDir, derr := confinement.DefaultStateDir()
	if derr != nil {
		return nil, nil, fmt.Errorf("cannot tell whether %s was launched confined (%v); refusing to start it with the caller's environment", child.Key(), derr)
	}
	confined := confinement.LaunchedConfined(stateDir, child.WorktreePath)
	if confined {
		if err := checkRelaunchBoundary(child, switchOn); err != nil {
			return nil, nil, err
		}
	}
	if !switchOn {
		return nil, nil, nil
	}
	kh, undo, err := keyholderLaunchEnvFn(home, child.RepoPath, child.Key(), true, stderr)
	if err != nil {
		return nil, nil, err
	}
	if kh == nil {
		return nil, nil, nil
	}
	fail := func(err error) ([]string, func(), error) {
		if undo != nil {
			undo()
		}
		return nil, nil, err
	}

	env := make(map[string]string, len(base))
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	env = withKeyholderEnv(env, kh)
	// clown finds the child's sidecar record under XDG_STATE_HOME; name the state
	// directory this frontend keeps its records under, whatever the caller had.
	env["XDG_STATE_HOME"] = stateDir
	// The caller's scope is the caller's: a child gets its own spec, or none.
	delete(env, scopeEnvVar)
	wantScope := ""
	if confined {
		wantScope = confinement.UnitSpecPath(stateDir, child.WorktreePath)
		env[scopeEnvVar] = wantScope
	}
	if err := checkHandoffEnv(env, wantScope); err != nil {
		return fail(err)
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out, undo, nil
}
