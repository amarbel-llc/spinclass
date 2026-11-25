package spawn

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"

	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// RetargetAgentSocket brings the stored unit spec of a confined worktree in line
// with the key holder switch as it is NOW, for a resume: a spec is written once,
// at spawn, so one written with the switch off still binds the operator's agent
// after the switch goes on, and one written with it on names a holder socket that
// no longer exists after it goes off.
//
//   - holderEnv non-nil (the switch is on and this launch got a pair): the
//     holder's socket becomes the spec's only agent socket and SSH_AUTH_SOCK, the
//     same shape spawn writes; the operator's agent bind and any earlier holder
//     path are dropped. A holder socket that is unusable refuses the resume.
//   - holderEnv nil: a spec that binds a holder socket is turned back into the
//     switch-off shape (the operator's socket when operatorSock names one, no
//     agent socket otherwise, and no SSH_AUTH_SOCK); a spec that binds none is left
//     untouched.
//
// The spec is read from and written to the frontend state dir the unit cannot
// write, atomically. It reports whether it rewrote the file.
func RetargetAgentSocket(stateDir, worktree string, holderEnv map[string]string, operatorSock string) (bool, error) {
	path := confinement.UnitSpecPath(stateDir, worktree)
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading the stored unit spec %s: %w", path, err)
	}
	var spec unitSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		if holderEnv == nil {
			// Nothing to take out of a spec this build cannot read; clown's own
			// parse of it is the check that matters for the switch-off shape.
			return false, nil
		}
		return false, fmt.Errorf("the stored unit spec %s does not parse (%v): refusing to resume a confined session from it with the key holder on", path, err)
	}
	before := slices.Clone(spec.Sockets)
	beforeEnv := maps.Clone(spec.Env)

	if holderEnv != nil {
		holder, err := keyHolderSocketFor(holderEnv)
		if err != nil {
			return false, err
		}
		spec.Sockets = []unitSocket{{Path: holder, Purpose: keyHolderSocketPurpose}}
		spec.Env = withEnvValue(spec.Env, "SSH_AUTH_SOCK", holder)
	} else {
		var oldHolder string
		for _, s := range spec.Sockets {
			if s.Purpose == keyHolderSocketPurpose {
				oldHolder = s.Path
			}
		}
		if oldHolder == "" {
			return false, nil
		}
		spec.Sockets = nil
		if operatorSock != "" {
			spec.Sockets = []unitSocket{{Path: operatorSock, Purpose: "piggy-agent"}}
		}
		if spec.Env["SSH_AUTH_SOCK"] == oldHolder {
			env := maps.Clone(spec.Env)
			delete(env, "SSH_AUTH_SOCK")
			spec.Env = env
		}
	}

	if slices.Equal(before, spec.Sockets) && maps.Equal(beforeEnv, spec.Env) {
		return false, nil
	}
	if _, err := confinement.WriteUnitSpec(stateDir, worktree, spec); err != nil {
		return false, fmt.Errorf("rewriting the stored unit spec %s: %w", path, err)
	}
	return true, nil
}

// withEnvValue returns a copy of env with key set; env is never modified.
func withEnvValue(env map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(env)+1)
	maps.Copy(out, env)
	out[key] = value
	return out
}
