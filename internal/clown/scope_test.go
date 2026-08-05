package clown

import (
	"context"
	"testing"
)

// ScopeUnitName derives the transient scope unit a job's hook runs under
// (RFC-0016 §3). Locking the exact string guards the derivation a stopper
// re-uses to address the same unit the producer created.
func TestScopeUnitName(t *testing.T) {
	got := ScopeUnitName("merge-9f3c1a2b")
	want := "ringmaster-merge-9f3c1a2b.scope"
	if got != want {
		t.Errorf("ScopeUnitName: got %q, want %q", got, want)
	}
}

// With RINGMASTER_DISABLE_SCOPE set the tier is off, so ScopeArgv must report
// unavailable and the caller runs the hook bare. Forced explicitly because a
// real Linux dev host may otherwise have a live user bus (available=true), which
// is not hermetically assertable — CI can only pin the disabled/bare path.
func TestScopeArgvDisabled(t *testing.T) {
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	if _, available := ScopeArgv("merge-9f3c1a2b"); available {
		t.Error("ScopeArgv reported available with RINGMASTER_DISABLE_SCOPE=1")
	}
}

// WithJobID / JobIDFromContext carry the async job id from job.Start to the
// pre-merge hook exec; a bare context yields "" so non-job hooks stay unscoped.
func TestJobIDContextRoundTrip(t *testing.T) {
	if got := JobIDFromContext(context.Background()); got != "" {
		t.Errorf("JobIDFromContext on a bare ctx: got %q, want empty", got)
	}
	ctx := WithJobID(context.Background(), "merge-9f3c1a2b")
	if got := JobIDFromContext(ctx); got != "merge-9f3c1a2b" {
		t.Errorf("JobIDFromContext: got %q, want %q", got, "merge-9f3c1a2b")
	}
}
