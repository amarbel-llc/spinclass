package main

import (
	"io"

	"code.linenisgreat.com/spinclass/internal/0/sessionlog"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// launchTerminals reports whether stdin and stdout are both terminals; tests
// replace it.
var launchTerminals = stdinStdoutAreTerminals

// launchKind is who a `sc start` / `sc resume` launch is for.
type launchKind int

const (
	// launchWorker: everything that is not the operator's own launch.
	launchWorker launchKind = iota
	// launchDriver: the operator at his terminal, with no session ancestry.
	launchDriver
)

func (k launchKind) String() string {
	if k == launchDriver {
		return "driver"
	}
	return "worker"
}

// classifyKeyholderLaunch is the ONE place that decides whether a launch is the
// operator's own (a DRIVER) or a worker's. A launch is the driver's only when
// ALL of these hold:
//   - the session is not a spawned worker: its record names no spawner
//     (spawned is session.State.EffectiveSpawner() != "", written by the
//     spawner and never by the session);
//   - nothing in the environment marks the caller as inside a session
//     (CLOWN_SESSION_ID, SPINCLASS_SESSION_ID: the same markers the card-act
//     commands use);
//   - stdin and stdout are both terminals.
//
// Nothing here can make a worker MORE privileged. A spawned worker is a worker
// whatever its environment or terminal says, because the spawn path never asks.
// A process that unsets the markers and allocates a pty and then runs
// `sc start` launches a session that inherits that process's own environment,
// which is already the holder's (or none), so it gains nothing it did not have.
func classifyKeyholderLaunch(spawned bool) launchKind {
	if spawned {
		return launchWorker
	}
	if profileGrantAgentRuntime() != "" {
		return launchWorker
	}
	if launchTerminals() {
		return launchDriver
	}
	return launchWorker
}

// keyholderLaunchPolicy is what the key holder does for a kind of launch,
// decided apart from the classification: StartsPair is "this session gets a
// pair", RewritesEnv is "its environment is pointed at the holder in place of
// the operator's agent". Rule A (today): a driver gets neither, because he
// still mints forge tokens and signs operator acts with his agent until that is
// delegated (decision 4). Rule B, a fast-follow, flips ONLY StartsPair for the
// driver (a pair beside his agent) and keeps RewritesEnv off; nothing else
// changes.
type keyholderLaunchPolicy struct {
	StartsPair  bool
	RewritesEnv bool
}

func keyholderPolicyFor(kind launchKind) keyholderLaunchPolicy {
	if kind == launchDriver {
		return keyholderLaunchPolicy{StartsPair: false, RewritesEnv: false}
	}
	return keyholderLaunchPolicy{StartsPair: true, RewritesEnv: true}
}

// resumeIsSpawned reports whether a session's record names a spawner: the
// spawn wrote it, a session cannot, and such a session is a worker for the
// key holder even when the operator resumes it at his terminal.
func resumeIsSpawned(st *session.State) bool {
	return st.EffectiveSpawner() != ""
}

// keyholderLaunchEnvUndo is the key holder's entry point for `sc start` and
// `sc resume`: it classifies the launch, looks up the policy for its kind, and
// applies it. The spawn path never comes through here: a spawned worker always
// goes to keyholderSessionEnvUndo.
func keyholderLaunchEnvUndo(home, repoPath, sessionKey string, spawned bool, stderr io.Writer) (map[string]string, func(), error) {
	kind := classifyKeyholderLaunch(spawned)
	if !keyholderPolicyFor(kind).StartsPair {
		sessionlog.Infof("keyholder: %s is a %s launch: no pair (it keeps the operator's agent)", sessionKey, kind)
	}
	return applyKeyholderPolicy(keyholderPolicyFor(kind), home, repoPath, sessionKey, stderr)
}

// applyKeyholderPolicy carries out a policy: no pair means no environment and
// nothing started; a pair without an environment rewrite starts (and reports an
// undo for) the pair and leaves the operator's environment alone.
func applyKeyholderPolicy(policy keyholderLaunchPolicy, home, repoPath, sessionKey string, stderr io.Writer) (map[string]string, func(), error) {
	if !policy.StartsPair {
		return nil, nil, nil
	}
	kh, undo, err := keyholderSessionEnvUndo(home, repoPath, sessionKey, stderr)
	if err != nil || policy.RewritesEnv {
		return kh, undo, err
	}
	return nil, undo, nil // a pair beside the operator's agent: environment untouched
}
