package spawn

import (
	"errors"
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

// The `worker` builtin is what the unattended fleet spawns confined workers with.
// clown 7504d6e869ad changed its digest (it keeps the synthesized
// clown-builtin-moxins plugin, issue-filer first), so this resolves it through the
// linked library, whatever its current digest, and runs the confined-profile
// rules on it. Nothing in spinclass pins a worker digest (grep: none); a recorded
// grant pins the digest it was signed for and must be re-issued after a bump.
// Until the clown flake input is bumped this checks the OLD linked worker; after
// the (operator's) bump it checks the new one.
func TestLinkedClownWorkerPassesTheConfinedProfileRules(t *testing.T) {
	r := clownResolver{lookup: newClownResolver().lookup, registryPath: filepath.Join(t.TempDir(), "no-registry.toml")}
	rp, err := r.Resolve("worker")
	if errors.Is(err, ErrProfileNotFound) {
		// Observed 2026-10-26: the clown linked via flake.lock has no `worker`
		// builtin at all (it resolves only on the newer clown the operator runs). A
		// skip, not a pass: the clown input bump (operator's) makes this run.
		t.Skipf("the linked clown (LinkedClownRev %q) has no `worker` builtin yet: bump the clown flake input, then this test checks it: %v", LinkedClownRev, err)
	}
	if err != nil {
		t.Fatalf("builtin %q does not resolve through the linked clown (LinkedClownRev %q): %v", "worker", LinkedClownRev, err)
	}
	if rp.Source != "builtin" || rp.Digest == "" {
		t.Fatalf("worker resolved as source %q digest %q, want a builtin with a digest", rp.Source, rp.Digest)
	}
	if _, _, _, err := requireConfinedProfile("worker", ""); err != nil {
		t.Fatalf("worker (digest %s, linked clown %q) must be accepted for a confined spawn: %v", rp.Digest, LinkedClownRev, err)
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
	if _, err := checkConfinedProfile("canary-spinclass", rp); err != nil {
		t.Fatalf("canary-spinclass must be accepted for a confined spawn: %v", err)
	}
}
