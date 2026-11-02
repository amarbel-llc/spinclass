package confinement

import "testing"

// The unit's HOME is the agent-home clown stages inside the worktree:
// clown's cmd/clown/agentscope.go builds it as
// filepath.Join(spec.Worktree, ".tmp", "agent-home") and
// internal/alfa/agentscope/unit.go sets HOME to it. spinclass cannot import
// clown's main package, so this pins spinclass's copy of that convention; the
// broker maps a confined session's home: anchor to it (spinclass#443). If
// clown moves its staging this test does not notice by itself: the live check
// (just explore-live-agent-home) and the follow-up that has clown record the
// path in the unit record are what close that gap.
func TestAgentHomeOfIsClownsStagingConvention(t *testing.T) {
	for in, want := range map[string]string{
		"/w/repo/.worktrees/me":  "/w/repo/.worktrees/me/.tmp/agent-home",
		"/w/repo/.worktrees/me/": "/w/repo/.worktrees/me/.tmp/agent-home",
	} {
		if got := AgentHomeOf(in); got != want {
			t.Errorf("AgentHomeOf(%q) = %q, want %q", in, got, want)
		}
	}
	if got := AgentHomeOf(""); got != "" {
		t.Errorf("no worktree means no agent-home, got %q", got)
	}
}
