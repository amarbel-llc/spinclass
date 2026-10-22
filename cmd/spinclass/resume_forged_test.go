package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"code.linenisgreat.com/spinclass/internal/session"
)

// A confined worker rewrites its worktree-local state.json: a different pinned
// entry and a principal. `sc resume` runs on the frontend and must
// replay the entry the frontend recorded, not the worker's.
func TestResumeIgnoresAForgedWorktreeRecord(t *testing.T) {
	f := newEntryFixture(t, "")
	pinned := &session.PinnedEntry{
		Start:  []string{"/bin/pinned-start"},
		Resume: []string{"/bin/pinned-resume"},
		Env:    map[string]string{"MARK": "pinned"},
	}
	f.writeState(t, pinned)

	forged := session.State{
		RepoPath: f.repo, WorktreePath: f.wt, Branch: "feat", SessionKey: "repo/feat",
		SessionState: session.StateInactive,
		Entry: &session.PinnedEntry{
			Start:  []string{"sh", "-c", "touch " + f.sentinel},
			Resume: []string{"sh", "-c", "touch " + f.sentinel},
			Env:    map[string]string{"MARK": "forged", "PATH": "/worktree/evil"},
		},
		Principal: "forged-principal",
	}
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.wt, ".spinclass", "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	c := stubAttach(t)

	if err := resumeFeat(t); err != nil {
		t.Fatalf("resume: %v", err)
	}
	se := capturedSessionExecutor(t, c)
	if !reflect.DeepEqual(se.Entrypoint, pinned.Resume) {
		t.Errorf("entrypoint = %v, want the frontend's pinned resume %v", se.Entrypoint, pinned.Resume)
	}
	if !reflect.DeepEqual(se.Env, pinned.Env) {
		t.Errorf("env = %v, want the frontend's pinned env %v", se.Env, pinned.Env)
	}
	if _, err := os.Stat(f.sentinel); err == nil {
		t.Error("a command named in the forged record ran")
	}
}
