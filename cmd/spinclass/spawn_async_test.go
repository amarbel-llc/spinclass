package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/spawn"
)

// TestAsyncSpawnResultText pins the spinclass#266 immediate-response contract:
// the async spawn result names the session key, worktree, and ringmaster job id,
// and tells the caller the hello arrives as a wake (end turn, do not poll).
func TestAsyncSpawnResultText(t *testing.T) {
	pending := spawn.Pending{SessionKey: "workerrepo/feat-1", WorktreePath: "/w/feat-1"}
	got := asyncSpawnResultText(pending, "driver/x", "spawn-abc123", 5*time.Minute)
	for _, want := range []string{
		"workerrepo/feat-1", // session key (chat address)
		"/w/feat-1",         // worktree
		"spawn-abc123",      // ringmaster job id
		"job-wakeup",
		"do not poll",
		"5m0s",     // hello-timeout
		"driver/x", // the driver the worker will message
	} {
		if !strings.Contains(got, want) {
			t.Errorf("asyncSpawnResultText missing %q:\n%s", want, got)
		}
	}
}

// TestSpawnTimeoutOutcomeKeepsBootingWorker: when the worker's spawn.log shows
// recent activity, the timeout outcome KEEPS the session (names it as possibly
// still booting) rather than reaping it (spinclass#266 decision 1). No git
// worktree is needed because the active-log branch returns before RunResolved.
func TestSpawnTimeoutOutcomeKeepsBootingWorker(t *testing.T) {
	wt := t.TempDir()
	spdir := filepath.Join(wt, ".spinclass")
	if err := os.MkdirAll(spdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A freshly written spawn.log ⇒ within spawnLogActiveWindow ⇒ "still booting".
	if err := os.WriteFile(filepath.Join(spdir, "spawn.log"), []byte("booting...\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pending := spawn.Pending{SessionKey: "workerrepo/feat-2", WorktreePath: wt}
	msg := spawnTimeoutOutcome(pending, 90*time.Second)

	if !strings.Contains(msg, "workerrepo/feat-2") || !strings.Contains(msg, "dangling") {
		t.Errorf("expected a keep+name message naming the session, got: %s", msg)
	}
	// The worktree must NOT have been reaped (the active-log branch never reaps).
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree should survive an active-log timeout, stat: %v", err)
	}
}
