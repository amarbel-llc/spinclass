package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// #435: list-handles and whoami say who granted each handle and when, mark a
// spawner's seeded handle as the spawn, and mark an old handle unrecorded.
func TestListHandlesShowsHandleProvenance(t *testing.T) {
	testgit.RequireGit(t)
	const caller = "caller-principal"
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", caller)
	t.Setenv("SPINCLASS_SESSION_ID", "")

	at := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	write := func(repoName string, mutate func(*session.State)) {
		repo := filepath.Join(t.TempDir(), repoName)
		testgit.MustInit(t, repo)
		wt := filepath.Join(repo, ".worktrees", "kid")
		testgit.MustWorktreeAdd(t, repo, wt, "kid")
		st := session.State{
			SessionState: session.StateInactive,
			RepoPath:     repo,
			WorktreePath: wt,
			Branch:       "kid",
			SessionKey:   repoName + "/kid",
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		}
		mutate(&st)
		if err := session.Write(st); err != nil {
			t.Fatal(err)
		}
	}
	// A pre-#480 record with the grant still pending: it reads back held,
	// accepted at its grant time (spinclass#480).
	write("granted-repo", func(st *session.State) {
		st.PendingHandles = []string{caller}
		st.HandleRights = map[string]string{caller: "observe"}
		st.HandleProvenance = map[string]session.HandleProvenance{caller: {GrantedBy: "granter-principal", GrantedAt: &at}}
	})
	write("spawned-repo", func(st *session.State) {
		st.SpawnedByPrincipal = caller
		st.Holders = []string{caller}
		st.SeedSpawnerHandle(caller, at)
	})
	write("old-repo", func(st *session.State) {
		st.Holders = []string{caller}
		st.HandleRights = map[string]string{caller: "observe,close"}
	})

	text, isErr := callListHandles(t, `{}`)
	if isErr {
		t.Fatalf("list-handles failed: %s", text)
	}
	for _, want := range []string{
		"held    granted-repo/kid  rights=observe  granted_by=granter-principal granted_at=2026-11-08T10:00:00Z accepted_at=2026-11-08T10:00:00Z",
		"held    spawned-repo/kid",
		"spawned_at=2026-11-08T10:00:00Z",
		"held    old-repo/kid  rights=observe,close  provenance=unrecorded",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("list-handles missing %q in:\n%s", want, text)
		}
	}

	if strings.Contains(text, "pending ") {
		t.Errorf("nothing is pending since #480, got:\n%s", text)
	}
}
