package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// A confined worker rewrites its worktree-local state.json to carry a sibling
// session's identity (repo, branch, worktree) plus a handle for itself. Accepting
// or granting a handle on the WORKER must write the worker's frontend record,
// never the sibling's.
func TestHandlesIgnoreAForgedWorktreeRecord(t *testing.T) {
	const caller = "caller-principal"
	repoPath, kidWt := childHandleFixture(t, caller, session.State{
		PendingHandles: []string{caller},
		HandleRights:   map[string]string{caller: "observe,close,grant"},
	})
	victimWt := filepath.Join(repoPath, ".worktrees", "victim")
	testgit.MustWorktreeAdd(t, repoPath, victimWt, "victim")
	victim := session.State{
		SessionState: session.StateInactive,
		RepoPath:     repoPath,
		WorktreePath: victimWt,
		Branch:       "victim",
		SessionKey:   "worker/victim",
		Holders:      []string{"victims-owner"},
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
	forged.PendingHandles = []string{caller}
	forged.HandleRights = map[string]string{caller: "observe,close,grant"}
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kidWt, ".spinclass", "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	if text, isErr := callListHandles(t, `{"accept":true}`); isErr {
		t.Fatalf("list-handles: %s", text)
	}
	// A grant that cannot find its target is reported below as a missing grant on
	// the worker's record; what must never happen is a write to the victim.
	if text, isErr := callGrant(t, `{"child":"worker/kid","to":"p9"}`); isErr {
		t.Errorf("grant: %s", text)
	}

	st, err := session.Read(repoPath, "victim")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(st.Holders, caller) || slices.Contains(st.PendingHandles, "p9") || !slices.Equal(st.Holders, []string{"victims-owner"}) {
		t.Errorf("the victim's record was written through the forged copy: holders=%v pending=%v", st.Holders, st.PendingHandles)
	}
	if got, _ := os.ReadFile(filepath.Join(victimWt, ".spinclass", "state.json")); string(got) != string(victimCopy) {
		t.Error("the victim's display copy was rewritten")
	}
	kid, err := session.Read(repoPath, "kid")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(kid.Holders, caller) || !slices.Contains(kid.PendingHandles, "p9") {
		t.Errorf("the worker's own record should carry the accept and the grant: holders=%v pending=%v", kid.Holders, kid.PendingHandles)
	}
}
