package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// A confined worker rewrites its own worktree-local state.json to name a sibling
// session (its repo, branch and worktree) under its own session key. The driver
// legitimately reaps the worker; the reap must close the WORKER, never the
// sibling the forged copy names.
func TestCloseChildIgnoresAForgedWorktreeRecord(t *testing.T) {
	const driverKey = "driver/main-oak"
	repoPath, kidWt := childFixture(t, driverKey, driverKey)

	victimWt := filepath.Join(repoPath, ".worktrees", "victim")
	testgit.MustWorktreeAdd(t, repoPath, victimWt, "victim")
	victim := session.State{
		SessionState: session.StateInactive,
		RepoPath:     repoPath,
		WorktreePath: victimWt,
		Branch:       "victim",
		SessionKey:   "worker/victim",
		SpawnedBy:    "other/elm",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}
	if err := session.Write(victim); err != nil {
		t.Fatal(err)
	}

	forged := victim
	forged.SessionKey = "worker/kid"
	forged.SpawnedBy = driverKey
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kidWt, ".spinclass", "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	callCloseChild(t, `{"child":"worker/kid"}`)

	if _, err := os.Stat(victimWt); err != nil {
		t.Errorf("the victim's worktree was removed: %v", err)
	}
	out, err := exec.Command("git", "-C", repoPath, "branch", "--list", "victim").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Errorf("the victim's branch was deleted (err=%v out=%q)", err, out)
	}
	st, err := session.Read(repoPath, "victim")
	if err != nil || st.IsTombstone() {
		t.Errorf("the victim's session record was closed or removed: %+v err=%v", st, err)
	}
	if _, err := os.Stat(kidWt); !os.IsNotExist(err) {
		t.Errorf("the worker itself should have been reaped (stat err = %v)", err)
	}
}
