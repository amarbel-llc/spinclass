package close

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// A session's key holder pair must not outlive the session: close stops it
// before any teardown, whatever the [keyholder] switch says now.
func TestRunResolvedStopsTheSessionsKeyHolder(t *testing.T) {
	repoPath, wtPath := setupNoTTYClose(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	prev := stopKeyholder
	t.Cleanup(func() { stopKeyholder = prev })
	var got string
	stopKeyholder = func(_ context.Context, key string) (bool, error) { got = key; return true, nil }

	if err := RunResolved(io.Discard, repoPath, wtPath, "feature-x", true, nil, "tap"); err != nil {
		t.Fatalf("RunResolved: %v", err)
	}
	if want := filepath.Base(repoPath) + "/feature-x"; got != want {
		t.Errorf("stopKeyholder called for %q, want the closing session %q", got, want)
	}
}

func TestStopSessionKeyholderFailsWithoutForceAndWarnsWithIt(t *testing.T) {
	prev := stopKeyholder
	t.Cleanup(func() { stopKeyholder = prev })
	stopKeyholder = func(context.Context, string) (bool, error) { return true, errors.New("fibby is still running") }

	err := stopSessionKeyholder(nil, "r/feature-x", "feature-x", false)
	if err == nil || !strings.Contains(err.Error(), "key holder") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("got %v, want a refusal naming the key holder and --force", err)
	}
	if err := stopSessionKeyholder(nil, "r/feature-x", "feature-x", true); err != nil {
		t.Errorf("--force must carry on: %v", err)
	}
}

// With no pair on disk the default seam does nothing and touches no system:
// a machine that never turned the switch on pays one stat.
func TestDefaultStopKeyholderIsANoOpWithoutAPair(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stopped, err := stopKeyholder(context.Background(), "r/none")
	if err != nil || stopped {
		t.Errorf("stopKeyholder with no pair = (%v, %v), want (false, nil)", stopped, err)
	}
	if _, serr := os.Lstat(keyholder.StateBase()); serr == nil {
		t.Error("stopping a session with no pair created the key holder state directory")
	}
}
