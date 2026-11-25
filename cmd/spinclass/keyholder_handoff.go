package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
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
// its own pair, the scrub, and, when it was launched confined, its own stored spec
// brought in line with the switch. It returns nil, nil, nil when the key holder is
// off: the hand-off is then run as it always was.
func startHandoffEnvFor(child *session.State, base []string, stderr io.Writer) ([]string, func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, fmt.Errorf("resolving the home directory for the key holder: %w", err)
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
	stateDir, derr := confinement.DefaultStateDir()
	if derr != nil {
		return fail(fmt.Errorf("cannot tell whether %s was launched confined (%v); with the key holder on, refusing to start it with the caller's environment", child.Key(), derr))
	}
	confined := confinement.LaunchedConfined(stateDir, child.WorktreePath)
	if confined {
		if _, rerr := spawn.RetargetAgentSocket(stateDir, child.WorktreePath, kh, os.Getenv("SSH_AUTH_SOCK")); rerr != nil {
			return fail(fmt.Errorf("%s was launched confined and its stored unit spec cannot be brought in line with the key holder: %w", child.Key(), rerr))
		}
	}

	env := make(map[string]string, len(base))
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	env = withKeyholderEnv(env, kh)
	// The caller's scope is the caller's: a child gets its own spec, or none.
	delete(env, scopeEnvVar)
	if confined {
		env[scopeEnvVar] = confinement.UnitSpecPath(stateDir, child.WorktreePath)
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out, undo, nil
}
