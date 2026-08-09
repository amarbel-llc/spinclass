package clown

import (
	"context"
	"strings"
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

// ScopeArgv returns the deterministic systemd-run prefix regardless of the
// availability bool (which only reports whether the tier is usable), so this
// runs in CI without a systemd user bus. Locking the prefix's key properties
// guards ringmaster#16's producer contract from the consumer side: the fast reap
// of a SIGTERM-ignoring subtree depends on `TimeoutStopSec` (the scope escalates
// to SIGKILL in seconds) and `KillMode=control-group` (the whole subtree is
// reaped); without them spinclass's ~10s ScopeStop ctx can't reap in time (see
// sweatfile's TestRunPreMergeHookScopeReapsSubtreeOnCancel). `--unit` pins the
// name ScopeStop later addresses, and the trailing `--` separates the prefix
// from the hook command.
func TestScopeArgvPrefix(t *testing.T) {
	jobID := "merge-9f3c1a2b"
	argv, _ := ScopeArgv(jobID)
	joined := strings.Join(argv, " ")

	for _, want := range []string{
		"systemd-run",
		"--user",
		"--scope",
		"--unit",
		ScopeUnitName(jobID),
		"KillMode=control-group",
		"TimeoutStopSec=3s",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("ScopeArgv prefix missing %q; got %v", want, argv)
		}
	}
	if n := len(argv); n == 0 || argv[n-1] != "--" {
		t.Errorf("ScopeArgv prefix must end with %q so the hook command follows; got %v", "--", argv)
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
