package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// A confined worker rewrites its worktree-local state.json to carry a credential
// with a revoke_command of its choosing and a vanished worktree, so the orphan
// sweep would read it as an abandoned session and run the command on the
// frontend. The record the frontend holds for the worker has no credential.
func TestSweepOrphansIgnoresAForgedWorktreeRecord(t *testing.T) {
	repoPath, wtPath, id := setupRepo(t)
	sentinel := filepath.Join(t.TempDir(), "sentinel")

	w1 := session.State{
		SessionState: session.StateActive,
		RepoPath:     repoPath,
		WorktreePath: wtPath,
		Branch:       id.Branch,
		SessionKey:   id.SessionKey,
		StartedAt:    time.Now().UTC(),
	}
	if err := session.Write(w1); err != nil {
		t.Fatal(err)
	}

	revoke := "touch " + sentinel
	forged := w1
	forged.WorktreePath = filepath.Join(repoPath, ".worktrees", "vanished")
	forged.Credential = &session.Credential{MintedAt: time.Now().UTC(), RevokeCommand: &revoke}
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wtPath, ".spinclass"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, ".spinclass", "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, errs := SweepOrphans(context.Background(), repoPath, nil); len(errs) > 0 {
		t.Logf("sweep errors: %v", errs)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("the sweep ran the revoke_command a worker wrote into its own state.json")
	}
}
