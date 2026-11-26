package spawn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// AgentBoundaryError is the refusal of a relaunch whose stored unit spec does not
// match the key holder switch as it is NOW. clown re-reads the stored spec FILE
// on every relaunch (a resume, a restart in place, a start hand-off) and compares
// its boundary digest with the one it recorded at the last confined launch; an
// UNCHANGED file always matches, so clown relaunches a stale spec (one that still
// binds the operator's agent after the switch went on) without a word. Its gate
// refuses only a file REPLACED in place with other socket purposes. So this check
// is the ONLY wall against a changed agent: the agent socket is part of the
// boundary, a changed agent is a changed boundary, and the honest answer is a new
// session. spinclass refuses before clown is called and before any pair is
// started for the relaunch, and says what to do.
type AgentBoundaryError struct {
	// Worktree is the confined worktree whose spec was checked.
	Worktree string
	// Stored says what the stored spec binds as the agent: "the operator's agent
	// socket", "a key holder socket" or "no agent socket".
	Stored string
	// SwitchOn is the key holder switch as it is now.
	SwitchOn bool
	// Detail is an extra reason when the spec could not be read or names a
	// different agent than the one it binds.
	Detail string
}

func (e *AgentBoundaryError) Error() string {
	mode := "off"
	if e.SwitchOn {
		mode = "on"
	}
	msg := fmt.Sprintf("the stored unit spec of %s binds %s, but [keyholder] mode is now %q: a changed agent socket is a changed boundary, and a session keeps the boundary it was spawned with. clown relaunches the stored spec as it is, so this check is what stops the old agent from continuing (clown's own gate refuses only a spec file replaced in place with other socket purposes, with its text \"a changed spec means a new session\"); the operator's agent is never rebound to a session started on its own key, nor a dead holder path to one started without",
		e.Worktree, e.Stored, mode)
	if e.Detail != "" {
		msg += " (" + e.Detail + ")"
	}
	return msg + ": close this session and spawn a new one"
}

// CheckAgentBoundary compares the agent socket a stored unit spec binds with the
// key holder switch as it is now and returns an *AgentBoundaryError when they
// disagree, in BOTH directions:
//
//   - switchOn: the stored spec must bind the session's key holder socket and
//     nothing else as an agent, and name it as SSH_AUTH_SOCK. A spec written with
//     the switch off binds the operator's agent (or none): refused.
//   - switchOff: the stored spec must bind no key holder socket. A spec written
//     with the switch on names a holder path that nothing listens on once the pair
//     is gone: refused.
//
// Nothing is written. The spec is read from the frontend state dir the unit
// cannot write. With the switch off, a spec that cannot be read or parsed is
// left to clown's own parse (it was never rewritten here); with the switch on it
// is refused.
func CheckAgentBoundary(stateDir, worktree string, switchOn bool) error {
	path := confinement.UnitSpecPath(stateDir, worktree)
	raw, err := os.ReadFile(path)
	if err != nil {
		if !switchOn {
			return nil
		}
		return &AgentBoundaryError{Worktree: worktree, Stored: "an unreadable spec", SwitchOn: true, Detail: err.Error()}
	}
	var spec unitSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		if !switchOn {
			return nil
		}
		return &AgentBoundaryError{Worktree: worktree, Stored: "a spec that does not parse", SwitchOn: true, Detail: err.Error()}
	}
	var holder string
	operator := false
	for _, s := range spec.Sockets {
		switch s.Purpose {
		case keyHolderSocketPurpose:
			holder = s.Path
		case operatorAgentSocketPurpose:
			operator = true
		}
	}
	stored := "no agent socket"
	switch {
	case operator:
		stored = "the operator's agent socket"
	case holder != "":
		stored = "a key holder socket"
	}
	if switchOn {
		if operator || holder == "" {
			return &AgentBoundaryError{Worktree: worktree, Stored: stored, SwitchOn: true}
		}
		if got := spec.Env["SSH_AUTH_SOCK"]; got != holder {
			return &AgentBoundaryError{
				Worktree: worktree, Stored: stored, SwitchOn: true,
				Detail: fmt.Sprintf("its SSH_AUTH_SOCK names %q, not the holder socket %s", got, holder),
			}
		}
		// The certificate directory is part of the same boundary (clown's digest
		// covers binds_ro): a holder spec written before the certificate interface
		// lacks it, and the answer is the same as for any other changed boundary.
		certDir := keyholder.CertDirOfAgentSock(holder)
		if !slices.Contains(spec.BindsRO, certDir) {
			return &AgentBoundaryError{
				Worktree: worktree, Stored: stored, SwitchOn: true,
				Detail: fmt.Sprintf("it does not bind the certificate directory %s read-only (a session spawned before the certificate interface)", certDir),
			}
		}
		if got, want := spec.Env[keyholder.CertEnv], filepath.Join(certDir, "cert.pub"); got != want {
			return &AgentBoundaryError{
				Worktree: worktree, Stored: stored, SwitchOn: true,
				Detail: fmt.Sprintf("its %s names %q, not %s", keyholder.CertEnv, got, want),
			}
		}
		return nil
	}
	if holder != "" {
		return &AgentBoundaryError{Worktree: worktree, Stored: stored, SwitchOn: false}
	}
	return nil
}

// operatorAgentSocketPurpose is the sockets[] purpose of the operator's own agent
// socket, as the switch-off builder writes it.
const operatorAgentSocketPurpose = "piggy-agent"
