package confinement

import "path/filepath"

// AgentHomeOf is the HOME of the agent unit launched for worktree: the
// per-session home clown stages at <worktree>/.tmp/agent-home and sets as HOME
// inside the unit (clown cmd/clown/agentscope.go stageAgentHome,
// internal/alfa/agentscope/unit.go). The worktree is bound into the unit at its
// own path, so this is the same path in the frontend and in the unit.
//
// It is a copy of clown's convention: clown's staging is in a main package
// spinclass cannot import, and the unit spec does not record the path. The
// broker uses it to give a confined session's home: anchor its own HOME
// (spinclass#443). Empty for an empty worktree.
func AgentHomeOf(worktree string) string {
	if worktree == "" {
		return ""
	}
	return filepath.Join(worktree, ".tmp", "agent-home")
}
