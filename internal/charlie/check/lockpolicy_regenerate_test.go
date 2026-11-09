package check

import (
	"context"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/clown"
)

// The regenerate phase admits a session's lock with the guarded gate's own
// policy. These tests pin what is different (the scope id and the scrubbed
// frontend marker reach the evaluation) and what is not (the same refusals).
func TestAdmitLockForRegenerateGivesTheEvaluationAScopeAndTheScrubbedMarker(t *testing.T) {
	dir := commitTree(t, map[string]string{"flake.nix": "{ outputs = _: {}; }", "flake.lock": remoteOnlyLock}, nil)
	specs := specsFromLock(t, remoteOnlyLock, "nixpkgs", "tb")
	var sawScope string
	var sawScrubbed bool
	prev := declaredFlakeInputs
	declaredFlakeInputs = func(ctx context.Context, _ string, _ []byte, _ []string) (map[string]inputSpec, error) {
		sawScope = clown.ScopeIDFromContext(ctx)
		sawScrubbed = scrubbedFrontend(ctx)
		return specs, nil
	}
	t.Cleanup(func() { declaredFlakeInputs = prev })

	if err := AdmitLockForRegenerate(context.Background(), dir, nil); err != nil {
		t.Fatalf("a lock that matches every declared input is admitted: %v", err)
	}
	if sawScope == "" {
		t.Error("the evaluation got no scope id, so the real one would refuse to run unscoped")
	}
	if !sawScrubbed {
		t.Error("the evaluation was not marked as running from a scrubbed frontend")
	}
	if scrubbedFrontend(context.Background()) {
		t.Error("the marker must not leak into the caller's context")
	}
}

func TestAdmitLockForRegenerateKeepsTheGatesRefusals(t *testing.T) {
	stubDeclaredInputs(t, specsFromLock(t, remoteOnlyLock, "nixpkgs", "tb"), nil)

	local := commitTree(t, map[string]string{"flake.nix": "{ outputs = _: {}; }", "flake.lock": pathInputLock}, nil)
	if err := AdmitLockForRegenerate(context.Background(), local, nil); err == nil || !strings.Contains(err.Error(), "leak: locked type path") {
		t.Errorf("a path-type lock node: err = %v, want the gate's refusal", err)
	}

	noLock := commitTree(t, map[string]string{"flake.nix": "{ outputs = _: {}; }"}, nil)
	if err := AdmitLockForRegenerate(context.Background(), noLock, nil); err == nil || !strings.Contains(err.Error(), "committed flake.lock") {
		t.Errorf("no lock: err = %v, want a refusal asking for a committed lock", err)
	}

	specs := specsFromLock(t, remoteOnlyLock, "nixpkgs")
	specs["extra"] = inputSpec{Original: github("x", "y"), Flake: true}
	stubDeclaredInputs(t, specs, nil)
	lacking := commitTree(t, map[string]string{"flake.nix": "{ outputs = _: {}; }", "flake.lock": remoteOnlyLock}, nil)
	if err := AdmitLockForRegenerate(context.Background(), lacking, nil); err == nil || !strings.Contains(err.Error(), "extra: missing from the lock") {
		t.Errorf("an input the lock lacks: err = %v, want the gate's refusal", err)
	}
}

// clown's flake.nix has `tommy.inputs.tap.inputs.gomod2nix.follows = ...`: an
// override of tap's OWN inputs that names no reference for tap. Nix keeps
// tommy's reference for tap, so it is not the indirect `flake:tap` a top-level
// input without a reference would be (the live refusal of 2026-11-09).
func TestLockPolicyAcceptsANestedOverrideThatNamesNoReference(t *testing.T) {
	dir := commitTree(t, map[string]string{"flake.nix": "{ outputs = _: {}; }", "flake.lock": nestedLock}, nil)
	specs := specsFromLock(t, nestedLock, "nixpkgs", "sub")
	sub := specs["sub"]
	sub.Inputs = map[string]inputSpec{"leak": {Inherits: true, Flake: true}}
	specs["sub"] = sub
	stubDeclaredInputs(t, specs, nil)
	if err := refuseUnsafeFlakeLock(context.Background(), dir, nil); err != nil {
		t.Fatalf("a nested override naming no reference must not be compared with the lock node: %v", err)
	}

	// Read as a top-level input would be (indirect), it is the mismatch the
	// policy used to report: the evaluation, not the Go, decides which it is.
	sub.Inputs = map[string]inputSpec{"leak": {Original: map[string]any{"type": "indirect", "id": "leak"}, Flake: true}}
	specs["sub"] = sub
	stubDeclaredInputs(t, specs, nil)
	if err := refuseUnsafeFlakeLock(context.Background(), dir, nil); err == nil || !strings.Contains(err.Error(), "sub/leak") {
		t.Fatalf("err = %v, want the indirect-vs-locked mismatch kept for a declared reference", err)
	}
}

// Inheriting the reference does not exempt what is declared below it: a nested
// override under a reference-less one is still checked against the lock.
func TestLockPolicyStillWalksOverridesBelowAnInheritedInput(t *testing.T) {
	dir := commitTree(t, map[string]string{"flake.nix": "{ outputs = _: {}; }", "flake.lock": nestedLock}, nil)
	specs := specsFromLock(t, nestedLock, "nixpkgs", "sub")
	sub := specs["sub"]
	sub.Inputs = map[string]inputSpec{"leak": {Inherits: true, Flake: true, Inputs: map[string]inputSpec{
		"deeper": {Original: github("x", "y"), Flake: true},
	}}}
	specs["sub"] = sub
	stubDeclaredInputs(t, specs, nil)
	if err := refuseUnsafeFlakeLock(context.Background(), dir, nil); err == nil || !strings.Contains(err.Error(), "sub/leak/deeper: missing from the lock") {
		t.Fatalf("err = %v, want the deeper override reported missing from the lock", err)
	}
}

func TestAdmitLockForRegenerateAdmitsATreeWithNoFlake(t *testing.T) {
	dir := commitTree(t, map[string]string{"README": "x"}, nil)
	if err := AdmitLockForRegenerate(context.Background(), dir, nil); err != nil {
		t.Errorf("no flake.nix means nothing to judge: %v", err)
	}
}
