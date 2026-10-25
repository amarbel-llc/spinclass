package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/trustedhome"
	"code.linenisgreat.com/spinclass/internal/profilerights"
)

type passingConfinement struct{}

func (passingConfinement) Check() error { return nil }

// An agent running `sc spawn` sets its own XDG_* dirs. The authorizer must not
// follow them to a grant store or signers file of the agent's choosing: it
// refuses, and creates nothing there (audit row 34-31).
func TestProductionAuthorizerRefusesAnXDGOverrideOfTheTrustAnchors(t *testing.T) {
	operatorHome := t.TempDir()
	original := trustedhome.Home
	t.Cleanup(func() { trustedhome.Home = original })
	trustedhome.Home = func() (string, error) { return operatorHome, nil }

	agentState := t.TempDir()
	t.Setenv("XDG_STATE_HOME", agentState)
	t.Setenv("XDG_CONFIG_HOME", "")

	auth := productionAuthorizer{confinementFor: func(Driver) profilerights.Confinement { return passingConfinement{} }}
	_, err := auth.Authorize(Driver{Principal: "p", SessionKey: "r/b"}, ResolvedProfile{Name: "worker", Digest: "d"})
	if err == nil || !strings.Contains(err.Error(), "XDG_STATE_HOME") {
		t.Fatalf("got %v, want a refusal naming XDG_STATE_HOME", err)
	}
	if _, statErr := os.Stat(filepath.Join(agentState, "spinclass", "grants")); !os.IsNotExist(statErr) {
		t.Errorf("a grant store was created under the agent's XDG_STATE_HOME (stat err %v)", statErr)
	}
}

func TestProductionAuthorizerRefusesAnXDGConfigOverride(t *testing.T) {
	operatorHome := t.TempDir()
	original := trustedhome.Home
	t.Cleanup(func() { trustedhome.Home = original })
	trustedhome.Home = func() (string, error) { return operatorHome, nil }

	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	auth := productionAuthorizer{confinementFor: func(Driver) profilerights.Confinement { return passingConfinement{} }}
	_, err := auth.Authorize(Driver{Principal: "p", SessionKey: "r/b"}, ResolvedProfile{Name: "worker", Digest: "d"})
	if err == nil || !strings.Contains(err.Error(), "XDG_CONFIG_HOME") {
		t.Fatalf("got %v, want a refusal naming XDG_CONFIG_HOME", err)
	}
}
