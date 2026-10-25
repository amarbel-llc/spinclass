package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// adoptFixture writes the frontend record of a freshly spawned confined worker
// (no hello yet) under a fake XDG_STATE_HOME and returns its Pending, the record
// as written, and the unit state dir the principal mapping lives in.
func adoptFixture(t *testing.T) (p Pending, rec session.State, unitState string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	t.Cleanup(session.SetReportSink(func(string) {}))
	repo := filepath.Join(base, "repo")
	wt := filepath.Join(repo, ".worktrees", "kid")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	rec = session.State{
		SessionState: session.StateInactive,
		RepoPath:     repo,
		WorktreePath: wt,
		Branch:       "kid",
		SessionKey:   "repo/kid",
		SpawnedBy:    "driver/key",
		StartedAt:    time.Now().UTC().Truncate(time.Second),
	}
	if err := session.Write(rec); err != nil {
		t.Fatal(err)
	}
	unitState = t.TempDir()
	p = Pending{
		SessionKey:   rec.SessionKey,
		WorktreePath: wt,
		RepoPath:     repo,
		Branch:       "kid",
		rp:           worktree.ResolvedPath{RepoPath: repo, AbsPath: wt, Branch: "kid", SessionKey: rec.SessionKey},
		confine:      &confinedLaunch{stateDir: unitState},
	}
	return p, rec, unitState
}

// forgeWorkerCopy overwrites the worker's display copy with hostile values on
// every field adoptConfinedWorker or the later frontend would otherwise act on.
func forgeWorkerCopy(t *testing.T, rec session.State, hello *time.Time) {
	t.Helper()
	forged := rec
	forged.HelloSentAt = hello
	forged.PID = 4242
	forged.Principal = "evil-principal"
	forged.Holders = []string{"evil-principal"}
	forged.SpawnedBy = "other/elm"
	forged.Profile = "evil-profile"
	forged.Entry = &session.PinnedEntry{Start: []string{"/bin/sh", "-c", "touch forged-entry"}}
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rec.WorktreePath, ".spinclass", "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// When the hello arrives the frontend record gets the hello marker, the active
// state and the principal the frontend mapping binds. Everything else the
// worker's display copy claims (entry, holders, spawner, profile, pid, its own
// hello time) is not adopted.
func TestAdoptConfinedWorkerRecordsOnlyWhatTheFrontendKnows(t *testing.T) {
	p, rec, unitState := adoptFixture(t)
	if err := confinement.RecordUnconfinedPrincipal(unitState, "real-principal", rec.SessionKey, rec.WorktreePath); err != nil {
		t.Fatal(err)
	}
	forgedHello := time.Now().Add(-48 * time.Hour).UTC()
	forgeWorkerCopy(t, rec, &forgedHello)
	before := time.Now().UTC().Add(-time.Second)

	adoptConfinedWorker(p)

	got, err := session.Read(rec.RepoPath, rec.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got.HelloSentAt == nil || got.HelloSentAt.Before(before) {
		t.Errorf("HelloSentAt = %v, want the frontend's own time of the hello (not the copy's %v)", got.HelloSentAt, forgedHello)
	}
	if got.SessionState != session.StateActive {
		t.Errorf("SessionState = %q, want active", got.SessionState)
	}
	if got.Principal != "real-principal" {
		t.Errorf("Principal = %q, want the mapping's real-principal", got.Principal)
	}
	if got.PID != 0 {
		t.Errorf("PID = %d, want none recorded for a confined worker", got.PID)
	}
	if got.Entry != nil || len(got.Holders) != 0 || got.Profile != "" || got.SpawnedBy != "driver/key" {
		t.Errorf("values only the display copy carried were adopted: entry=%v holders=%v profile=%q spawned_by=%q", got.Entry, got.Holders, got.Profile, got.SpawnedBy)
	}
	if got.RepoPath != rec.RepoPath || got.WorktreePath != rec.WorktreePath || got.Branch != "kid" {
		t.Errorf("identity moved: %s %s %s", got.RepoPath, got.WorktreePath, got.Branch)
	}
}

// With no frontend mapping for the worker, the principal its copy claims is not
// adopted: the record's principal stays empty.
func TestAdoptConfinedWorkerWithoutAMappingAdoptsNoPrincipal(t *testing.T) {
	p, rec, _ := adoptFixture(t)
	forgeWorkerCopy(t, rec, nil)

	adoptConfinedWorker(p)

	got, err := session.Read(rec.RepoPath, rec.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got.Principal != "" {
		t.Errorf("Principal = %q, want none without a frontend mapping", got.Principal)
	}
	if got.HelloSentAt == nil || got.SessionState != session.StateActive {
		t.Errorf("the hello was not recorded: hello=%v state=%q", got.HelloSentAt, got.SessionState)
	}
	if got.Entry != nil || len(got.Holders) != 0 {
		t.Errorf("values only the display copy carried were adopted: entry=%v holders=%v", got.Entry, got.Holders)
	}
}

// A hello time already in the frontend record is kept; the copy cannot move it.
func TestAdoptConfinedWorkerKeepsTheRecordedHelloTime(t *testing.T) {
	p, rec, _ := adoptFixture(t)
	first := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	rec.HelloSentAt = &first
	if err := session.Write(rec); err != nil {
		t.Fatal(err)
	}
	later := time.Now().UTC()
	forgeWorkerCopy(t, rec, &later)

	adoptConfinedWorker(p)

	got, err := session.Read(rec.RepoPath, rec.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got.HelloSentAt == nil || !got.HelloSentAt.Equal(first) {
		t.Errorf("HelloSentAt = %v, want the recorded %v", got.HelloSentAt, first)
	}
}
