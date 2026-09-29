package main

import (
	"regexp"
	"testing"
)

// uuidShape is the per-instance key shape clown mints (the claude
// --session-id UUID); the per-process fallback must be indistinguishable
// from it so nothing downstream can tell the two apart (FDR 0032 D1).
var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestCurrentPrincipalReadsClownKey pins FDR 0032 D1: when clown hosts the
// serve process, its per-instance key IS the principal, returned verbatim.
func TestCurrentPrincipalReadsClownKey(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "1d3a5c7e-9b0f-4d2a-8e6c-0a1b2c3d4e5f")
	t.Setenv("SPINCLASS_SESSION_ID", "")
	t.Chdir(t.TempDir()) // not a git repo: identity must not depend on cwd

	if got := currentPrincipal(); got != "1d3a5c7e-9b0f-4d2a-8e6c-0a1b2c3d4e5f" {
		t.Fatalf("currentPrincipal() = %q, want the clown per-instance key", got)
	}
}

// TestCurrentPrincipalFallbackIsStableAndLocationFree pins the no-clown
// fallback: a per-process random id in the same UUID shape, stable across
// calls (the hello target and the holder record depend on that), and
// resolved without touching git or the cwd — the exact gap behind
// spinclass#332.
func TestCurrentPrincipalFallbackIsStableAndLocationFree(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "")
	t.Setenv("SPINCLASS_SESSION_ID", "")
	t.Chdir(t.TempDir()) // not a git repo

	first := currentPrincipal()
	if !uuidShape.MatchString(first) {
		t.Fatalf("fallback principal %q is not UUID-shaped", first)
	}
	t.Chdir(t.TempDir()) // a different non-repo cwd: identity must not move
	if second := currentPrincipal(); second != first {
		t.Fatalf("principal flipped across calls: %q then %q", first, second)
	}
}
