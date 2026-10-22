package spawn

import (
	"path/filepath"
	"testing"
)

// canaryBuiltinProfiles are the clown builtin profiles the confinement canary
// rounds spawn with. spinclass resolves profiles through the clown library it
// LINKS at build time (flake.lock's clown input), not the installed clown, so a
// builtin added to clown is invisible here until the input is bumped. This test
// fails on a stale link instead of a live round refusing "profile not found".
var canaryBuiltinProfiles = []string{"canary", "canary-grit", "canary-spinclass"}

func TestLinkedClownHasTheCanaryBuiltins(t *testing.T) {
	r := clownResolver{lookup: newClownResolver().lookup, registryPath: filepath.Join(t.TempDir(), "no-registry.toml")}
	for _, name := range canaryBuiltinProfiles {
		rp, err := r.Resolve(name)
		if err != nil {
			t.Errorf("builtin %q does not resolve through the linked clown (LinkedClownRev %q): %v; bump the clown flake input", name, LinkedClownRev, err)
			continue
		}
		if rp.Source != "builtin" {
			t.Errorf("%q resolved from source %q, want builtin", name, rp.Source)
		}
	}
}

// canary-spinclass exists for confined rounds, so it must pass the confined
// profile rules (FDR 0035 R3 form (i), R4).
func TestCanarySpinclassPassesTheConfinedProfileRules(t *testing.T) {
	r := clownResolver{lookup: newClownResolver().lookup, registryPath: filepath.Join(t.TempDir(), "no-registry.toml")}
	rp, err := r.Resolve("canary-spinclass")
	if err != nil {
		t.Fatalf("resolving canary-spinclass: %v", err)
	}
	if err := checkConfinedProfile("canary-spinclass", rp); err != nil {
		t.Fatalf("canary-spinclass must be accepted for a confined spawn: %v", err)
	}
}
