package confinement

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/session"
)

// ForDriver is the production confinement check for one spawning session
// (profilerights.Confinement): every FDR 0035 fact, probed from this
// (frontend) process into the driver's agent unit.
type ForDriver struct {
	SessionKey string
	// Principal is the driver's principal as the frontend knows it (its own
	// CLOWN_SESSION_ID); the recorded one is used when empty.
	Principal string
}

// Check runs every probe and fails unless all hold. Failing to assemble the
// inputs is itself a failure.
func (d ForDriver) Check() error {
	in, err := InputsForDriver(d.SessionKey, d.Principal)
	if err != nil {
		return fmt.Errorf("session confinement (FDR 0035) cannot be checked: %w", err)
	}
	return Run(Host{}, in).Err()
}

// InputsForDriver assembles the check's inputs for a driver session from
// session state, git and this process's environment.
func InputsForDriver(sessionKey, principal string) (Inputs, error) {
	if sessionKey == "" {
		return Inputs{}, fmt.Errorf("the spawner has no session, so it has no agent unit to check")
	}
	states, err := session.ListAll(nil)
	if err != nil {
		return Inputs{}, fmt.Errorf("listing sessions: %w", err)
	}
	in := Inputs{ClaudeComms: DefaultClaudeComms}
	found := false
	for _, st := range states {
		if st.IsTombstone() {
			continue
		}
		s := Session{Key: st.SessionKey, Worktree: st.WorktreePath, PID: st.PID, Principal: st.Principal}
		if st.SessionKey == sessionKey {
			if principal != "" {
				s.Principal = principal
			}
			in.Driver, found = s, true
		}
		if session.IsAlive(st.PID) {
			in.Live = append(in.Live, s)
		}
	}
	if !found {
		return Inputs{}, fmt.Errorf("no session %q", sessionKey)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return Inputs{}, fmt.Errorf("finding the operator's home: %w", err)
	}
	in.OperatorHome = home
	in.AgentSocket = os.Getenv("SSH_AUTH_SOCK")
	in.XAuthority = os.Getenv("XAUTHORITY")
	in.StateDir = os.Getenv("XDG_STATE_HOME")
	if in.StateDir == "" {
		in.StateDir = filepath.Join(home, ".local", "state")
	}
	in.RuntimeAllow = RuntimeAllowFor(fmt.Sprintf("/run/user/%d", os.Getuid()), in.AgentSocket)

	gcd, err := git.Run(in.Driver.Worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Inputs{}, fmt.Errorf("finding the driver repo's git dir: %w", err)
	}
	in.GitCommonDir = gcd
	// An unset core.hooksPath exits 1; that is the common case, not an error.
	if hp, err := git.Run(in.Driver.Worktree, "config", "--get", "core.hooksPath"); err == nil && hp != "" {
		if !filepath.IsAbs(hp) {
			hp = filepath.Join(in.Driver.Worktree, hp)
		}
		in.HooksPath = hp
	}
	return in, nil
}

// runtimeBound are the operator-runtime-dir entries clown binds into every
// unit: its per-unit MCP sockets (clown/agent/<unit>/) and ringmaster's
// journal sockets (clown FDR 0020).
var runtimeBound = []string{"clown", "ringmaster"}

// RuntimeAllowFor lists what F14 lets the unit see in the operator's runtime
// dir: runtimeBound, plus the agent socket's top entry when it lives there.
func RuntimeAllowFor(runtimeDir, agentSocket string) []string {
	allow := slices.Clone(runtimeBound)
	if rel, err := filepath.Rel(runtimeDir, agentSocket); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
		first, _, _ := strings.Cut(rel, "/")
		if !slices.Contains(allow, first) {
			allow = append(allow, first)
		}
	}
	return allow
}
