package spawn

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSpawnLogActiveWithin covers the reap-if-dead liveness heuristic
// (spinclass#266): a missing log reads as not-active (reapable), a fresh log as
// active (still booting), and a log older than the window as not-active.
func TestSpawnLogActiveWithin(t *testing.T) {
	wt := t.TempDir()

	// No spawn.log yet → not active (reapable).
	if SpawnLogActiveWithin(wt, time.Minute) {
		t.Error("missing spawn.log should read as NOT active")
	}

	spdir := filepath.Join(wt, ".spinclass")
	if err := os.MkdirAll(spdir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(spdir, "spawn.log")
	if err := os.WriteFile(logPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Freshly written → active.
	if !SpawnLogActiveWithin(wt, time.Minute) {
		t.Error("fresh spawn.log should read as active")
	}

	// Backdated beyond the window → not active.
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatal(err)
	}
	if SpawnLogActiveWithin(wt, time.Minute) {
		t.Error("stale spawn.log should read as NOT active")
	}
}
