package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// runScClose drives `sc close <target>` through the real command wiring.
func runScClose(t *testing.T, target string) error {
	t.Helper()
	return buildApp().RunCLI(context.Background(), []string{"close", target}, nil)
}

// A confined worker rewrites its own worktree-local state.json to name a sibling
// session (its repo, branch and worktree) under its own session key. `sc close`
// of the worker must close the WORKER, never the sibling the forged copy names.
func TestScCloseIgnoresAForgedWorktreeRecord(t *testing.T) {
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
		Principal:    "victim-principal",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}
	if err := session.Write(victim); err != nil {
		t.Fatal(err)
	}
	victimCopy, err := os.ReadFile(filepath.Join(victimWt, ".spinclass", "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	forged := victim
	forged.SessionKey = "worker/kid"
	forged.Principal = "evil-principal"
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kidWt, ".spinclass", "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runScClose(t, "worker/kid"); err != nil {
		t.Fatalf("sc close worker/kid: %v", err)
	}

	if _, err := os.Stat(victimWt); err != nil {
		t.Errorf("the victim's worktree was removed: %v", err)
	}
	out, err := exec.Command("git", "-C", repoPath, "branch", "--list", "victim").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Errorf("the victim's branch was deleted (err=%v out=%q)", err, out)
	}
	st, err := session.Read(repoPath, "victim")
	if err != nil || st.IsTombstone() || st.Principal != "victim-principal" {
		t.Errorf("the victim's session record was closed, removed or rewritten: %+v err=%v", st, err)
	}
	if got, _ := os.ReadFile(filepath.Join(victimWt, ".spinclass", "state.json")); string(got) != string(victimCopy) {
		t.Error("the victim's display copy was rewritten")
	}
	if _, err := os.Stat(kidWt); !os.IsNotExist(err) {
		t.Errorf("the worker itself should have been closed (stat err = %v)", err)
	}
	if kid, err := session.Read(repoPath, "kid"); err != nil || !kid.IsTombstone() {
		t.Errorf("the worker's own record should be a tombstone: %+v err=%v", kid, err)
	}
}

// A confined worker rewrites its worktree copy to carry a credential whose
// revoke_command touches a sentinel. `sc close` revokes from the frontend record
// (the command pinned at mint), so the worker's command must not run.
func TestScCloseDoesNotRunAForgedRevokeCommand(t *testing.T) {
	const driverKey = "driver/main-oak"
	repoPath, kidWt := childFixture(t, driverKey, driverKey)
	dir := t.TempDir()
	good, evil := filepath.Join(dir, "good"), filepath.Join(dir, "evil")

	pinned := "touch " + good
	if err := session.Update(repoPath, "kid", func(st *session.State) error {
		st.Credential = &session.Credential{MintedAt: time.Now().UTC(), RevokeCommand: &pinned}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	st, err := session.Read(repoPath, "kid")
	if err != nil {
		t.Fatal(err)
	}
	forgedRevoke := "touch " + evil
	forged := *st
	forged.Credential = &session.Credential{MintedAt: time.Now().UTC(), RevokeCommand: &forgedRevoke}
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kidWt, ".spinclass", "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runScClose(t, "worker/kid"); err != nil {
		t.Fatalf("sc close worker/kid: %v", err)
	}

	if _, err := os.Stat(good); err != nil {
		t.Errorf("the revoke command pinned in the frontend record did not run: %v", err)
	}
	if _, err := os.Stat(evil); err == nil {
		t.Error("sc close ran the revoke_command a worker wrote into its own state.json")
	}
}
