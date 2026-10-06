package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/spawn"
	"code.linenisgreat.com/spinclass/internal/testgit"
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
	msg := spawnTimeoutOutcome(pending, "driver-principal", 90*time.Second)

	if !strings.Contains(msg, "workerrepo/feat-2") || !strings.Contains(msg, "dangling") {
		t.Errorf("expected a keep+name message naming the session, got: %s", msg)
	}
	// The worktree must NOT have been reaped (the active-log branch never reaps).
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree should survive an active-log timeout, stat: %v", err)
	}
}

// TestSpawnTimeoutOutcomeEmitsCrashOnAutoReap pins FDR 0032 D6: a successful
// auto-reap of a never-helloed, looked-dead worker emits "crash" to its
// Holders OTHER than the driver — the driver already learns via its own job
// wake (awaitSpawnHello's FinishJob). Stubs emitExitWakesFn rather than going
// through clown.EmitExitWakes, which would silently no-op with CLOWN_BIN
// unset in this test env.
func TestSpawnTimeoutOutcomeEmitsCrashOnAutoReap(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repoPath := filepath.Join(t.TempDir(), "worker")
	testgit.MustInit(t, repoPath)
	if err := os.WriteFile(filepath.Join(repoPath, "sweatfile"), []byte("[hooks]\ndisable-nix-gc = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtPath := filepath.Join(repoPath, ".worktrees", "feat-3")
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "feat-3")
	if err := os.WriteFile(filepath.Join(repoPath, ".git", "info", "exclude"), []byte(".spinclass/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const driverPrincipal = "principal-driver"
	if err := session.Write(session.State{
		SessionState:       session.StateInactive,
		RepoPath:           repoPath,
		WorktreePath:       wtPath,
		Branch:             "feat-3",
		SessionKey:         "worker/feat-3",
		SpawnedByPrincipal: driverPrincipal,
		Holders:            []string{driverPrincipal, "sibling-a"},
		Entrypoint:         []string{"/bin/sh"},
		StartedAt:          time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	var gotHolders []string
	var gotChildKey string
	var gotReason clown.ExitReason
	orig := emitExitWakesFn
	emitExitWakesFn = func(holders []string, childKey string, reason clown.ExitReason) error {
		gotHolders = holders
		gotChildKey = childKey
		gotReason = reason
		return nil
	}
	t.Cleanup(func() { emitExitWakesFn = orig })

	pending := spawn.Pending{SessionKey: "worker/feat-3", RepoPath: repoPath, WorktreePath: wtPath, Branch: "feat-3"}
	msg := spawnTimeoutOutcome(pending, driverPrincipal, 90*time.Second)

	if !strings.Contains(msg, "reaped") {
		t.Fatalf("expected a reap outcome message, got: %s", msg)
	}
	if gotReason != clown.ExitCrash {
		t.Errorf("reason = %q, want %q", gotReason, clown.ExitCrash)
	}
	if gotChildKey != "worker/feat-3" {
		t.Errorf("childKey = %q, want %q", gotChildKey, "worker/feat-3")
	}
	if got := strings.Join(gotHolders, ","); got != "sibling-a" {
		t.Errorf("holders = %q, want %q (driver excluded)", got, "sibling-a")
	}
}
