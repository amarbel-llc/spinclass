package spawn

import (
	"errors"
	"strings"
	"testing"
)

func stackProfiles() (base, child, grandchild ResolvedProfile) {
	base = ResolvedProfile{
		Name: "manager-builder", Digest: "d1", Provider: "claude", EnvKnown: true,
		KeptServers: []string{"just-us", "spinclass"},
	}
	child = ResolvedProfile{
		Name: "engineer-reviewer", Digest: "d2", Provider: "claude", EnvKnown: true,
		KeptServers: []string{"just-us"}, Base: "manager-builder", BaseProfile: &base,
	}
	grandchild = ResolvedProfile{
		Name: "reviewer-lite", Digest: "d3", Provider: "claude", EnvKnown: true,
		KeptServers: []string{"just-us"}, Base: "engineer-reviewer", BaseProfile: &child,
	}
	return base, child, grandchild
}

func resolveFrom(profiles ...ResolvedProfile) func(string) (ResolvedProfile, error) {
	return func(name string) (ResolvedProfile, error) {
		for _, p := range profiles {
			if p.Name == name {
				return p, nil
			}
		}
		return ResolvedProfile{}, errors.New("not found")
	}
}

// D1 stacking input: the chain is the bases clown resolved with the profile,
// nearest first, each with the step below's catalog widenings (none for a
// narrowing).
func TestBaseChainViaFollowsTheResolvedBases(t *testing.T) {
	base, child, grandchild := stackProfiles()
	chain, err := baseChainVia(resolveFrom(base, child, grandchild))("reviewer-lite", "d3")
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 || chain[0].Name != "engineer-reviewer" || chain[0].Digest != "d2" ||
		chain[1].Name != "manager-builder" || chain[1].Digest != "d1" || len(chain[0].Widenings)+len(chain[1].Widenings) != 0 {
		t.Fatalf("chain = %+v", chain)
	}
	if chain, err := baseChainVia(resolveFrom(base))("manager-builder", "d1"); err != nil || len(chain) != 0 {
		t.Errorf("a profile with no base: %+v, %v", chain, err)
	}
}

// A step that widens its base carries the widening, so Narrows refuses it.
func TestBaseChainViaReportsWidenings(t *testing.T) {
	base, child, _ := stackProfiles()
	child.KeptServers = []string{"just-us", "spinclass", "moxy"}
	chain, err := baseChainVia(resolveFrom(base, child))("engineer-reviewer", "d2")
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 1 || !strings.Contains(strings.Join(chain[0].Widenings, "; "), "adds server moxy") {
		t.Fatalf("chain = %+v, want the moxy widening on the first edge", chain)
	}
}

// Fail closed: a definition whose live digest moved, or whose base the
// resolver did not report, yields no chain.
func TestBaseChainViaFailsClosed(t *testing.T) {
	base, child, _ := stackProfiles()
	if _, err := baseChainVia(resolveFrom(base, child))("engineer-reviewer", "d-old"); err == nil || !strings.Contains(err.Error(), "live definition is d2") {
		t.Errorf("moved digest: %v", err)
	}
	child.BaseProfile = nil
	if _, err := baseChainVia(resolveFrom(base, child))("engineer-reviewer", "d2"); err == nil || !strings.Contains(err.Error(), "did not report its definition") {
		t.Errorf("unreported base: %v", err)
	}
	if _, err := baseChainVia(resolveFrom())("ghost", "d"); err == nil {
		t.Error("an unresolvable profile yielded a chain")
	}
}
