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

// writeLeftovers puts a session's key history (and optionally its kind record
// and lock file) on disk under the state root, with no pair directory: what an
// operator's `sc keyholder-stop --all` leaves behind.
func writeLeftovers(t *testing.T, key string, kind, lock bool) string {
	t.Helper()
	base := keyholder.StateBase()
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	hist := keyholder.LastPath(base, key)
	if err := os.WriteFile(hist, []byte(`{"fingerprint":"SHA256:old","stopped":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if kind {
		if err := os.WriteFile(keyholder.KindPath(base, key), []byte("worker\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if lock {
		if err := os.WriteFile(filepath.Join(base, keyholder.Hash(key)+".lock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return hist
}

// Closing a session after the operator stopped its pair (the pair directory is
// gone, the key history is kept marked stopped) ends the history: a later
// session with the same key must not be told its first key replaced the old one.
func TestCloseAfterAnOperatorStopEndsTheKeyHistoryAndTheKindRecord(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const key = "r/reused"
	hist := writeLeftovers(t, key, true, true)

	stopped, err := stopKeyholder(context.Background(), key)
	if err != nil || stopped {
		t.Fatalf("stopKeyholder = (%v, %v), want (false, nil): there was no pair directory", stopped, err)
	}
	base := keyholder.StateBase()
	for _, p := range []string{hist, keyholder.KindPath(base, key), filepath.Join(base, keyholder.Hash(key)+".lock")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s survived the close of the session", p)
		}
	}
}

// A driver has no pair at all: its kind record alone ends with the session.
func TestCloseOfADriverEndsItsKindRecordAlone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const key = "r/driver"
	base := keyholder.StateBase()
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyholder.KindPath(base, key), []byte("driver\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stopKeyholder(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(keyholder.KindPath(base, key)); err == nil {
		t.Error("the driver's kind record survived the close")
	}
}

// The close of one session leaves another session's history alone.
func TestCloseLeavesAnotherSessionsKeyHistoryAlone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	other := writeLeftovers(t, "r/other", true, false)
	writeLeftovers(t, "r/closing", true, true)
	if _, err := stopKeyholder(context.Background(), "r/closing"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(other); err != nil {
		t.Errorf("another session's key history was removed: %v", err)
	}
}
