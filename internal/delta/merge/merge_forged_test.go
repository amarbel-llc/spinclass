package merge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// A confined worker rewrites its own worktree-local state.json to name a sibling
// session (its repo, branch and worktree) and to carry a revoke_command. The
// operator then lands the WORKER with an out-of-session merge. The teardown after
// the landing (revoke credential, remove worktree, delete branch) must act on the
// worker alone: the sibling's worktree, branch, record and revoke command are
// untouched, and the worker's pinned revoke command, not its forged one, runs.
func TestMergeTeardownIgnoresAForgedWorktreeRecord(t *testing.T) {
	_, repoDir := setupSyncRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, ".git", "info", "exclude"), []byte(".spinclass/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	good, evil, victimRevoked := filepath.Join(dir, "good"), filepath.Join(dir, "evil"), filepath.Join(dir, "victim-revoked")

	victimWt := setupWorktree(t, repoDir, "victim")
	victimRevoke := "touch " + victimRevoked
	victim := session.State{
		SessionState: session.StateInactive,
		RepoPath:     repoDir,
		WorktreePath: victimWt,
		Branch:       "victim",
		SessionKey:   "repo/victim",
		Principal:    "victim-principal",
		StartedAt:    time.Now().UTC(),
		Credential: &session.Credential{
			MintedAt:      time.Now().UTC(),
			RevokeCommand: &victimRevoke,
			Remote:        &session.CredentialRemote{CredentialHost: "example.com", HTTPS: "https://example.com/"},
		},
	}
	if err := session.Write(victim); err != nil {
		t.Fatal(err)
	}
	victimCopy := readFileOrFatal(t, filepath.Join(victimWt, ".spinclass", "state.json"))

	workerWt := setupWorktree(t, repoDir, "worker")
	if err := os.WriteFile(filepath.Join(workerWt, "w.txt"), []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workerWt, "add", "w.txt")
	runGit(t, workerWt, "commit", "-m", "worker commit")
	pinned := "touch " + good
	writeMintedStatePinned(t, repoDir, workerWt, "worker", &pinned)

	forged := victim
	forgedRevoke := "touch " + evil
	forged.Credential = &session.Credential{MintedAt: time.Now().UTC(), RevokeCommand: &forgedRevoke}
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerWt, ".spinclass", "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	recs, err := runResolved(t, &mockExecutor{}, repoDir, workerWt, "worker", "main", true, false)
	if err != nil {
		t.Fatalf("Resolved() error: %v\n%+v", err, recs)
	}

	if _, err := os.Stat(workerWt); !os.IsNotExist(err) {
		t.Errorf("the worker's own worktree should have been torn down (stat err = %v)", err)
	}
	if _, err := os.Stat(victimWt); err != nil {
		t.Errorf("the victim's worktree was removed: %v", err)
	}
	if got := runGit(t, repoDir, "branch", "--list", "victim"); got == "" {
		t.Error("the victim's branch was deleted")
	}
	if got := runGit(t, repoDir, "branch", "--list", "worker"); got != "" {
		t.Errorf("the worker's branch should have been deleted, got %q", got)
	}
	st, err := session.Read(repoDir, "victim")
	if err != nil || st.IsTombstone() || st.Principal != "victim-principal" {
		t.Errorf("the victim's session record was closed, removed or rewritten: %+v err=%v", st, err)
	}
	if got := readFileOrFatal(t, filepath.Join(victimWt, ".spinclass", "state.json")); string(got) != string(victimCopy) {
		t.Error("the victim's display copy was rewritten")
	}
	if _, err := os.Stat(good); err != nil {
		t.Errorf("the worker's pinned revoke command did not run: %v", err)
	}
	for name, path := range map[string]string{"the worker's forged": evil, "the victim's": victimRevoked} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s revoke command ran at the merge teardown", name)
		}
	}
}

func readFileOrFatal(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
