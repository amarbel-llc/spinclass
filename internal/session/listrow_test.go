package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// listRowFixtures returns three states with deterministic resolved
// states: one active (live PID, worktree on disk), one inactive
// (worktree on disk), and one abandoned (worktree path missing).
func listRowFixtures(t *testing.T) []State {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "spinclass")
	active := filepath.Join(repo, ".worktrees", "crisp-catalpa")
	inactive := filepath.Join(repo, ".worktrees", "mellow-mango")
	for _, dir := range []string{active, inactive} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return []State{
		{
			PID:          os.Getpid(),
			SessionState: StateActive,
			RepoPath:     repo,
			WorktreePath: active,
			Branch:       "crisp-catalpa",
			SessionKey:   "spinclass/crisp-catalpa",
			Description:  "remote sessions",
		},
		{
			SessionState: StateInactive,
			RepoPath:     repo,
			WorktreePath: inactive,
			Branch:       "mellow-mango",
			SessionKey:   "spinclass/mellow-mango",
		},
		{
			SessionState: StateActive,
			RepoPath:     repo,
			WorktreePath: filepath.Join(repo, ".worktrees", "gone"),
			Branch:       "gone",
			SessionKey:   "spinclass/gone",
			Description:  "externally removed",
		},
	}
}

// TestListRowsWireFormat locks the `sc list --format json` row shape —
// the remote wire format. Field names and order are a contract.
func TestListRowsWireFormat(t *testing.T) {
	rows := ListRows(listRowFixtures(t), false)

	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	want := `[` +
		`{"id":"crisp-catalpa","session_key":"spinclass/crisp-catalpa","state":"active","description":"remote sessions","repo":"spinclass","branch":"crisp-catalpa"},` +
		`{"id":"mellow-mango","session_key":"spinclass/mellow-mango","state":"inactive","description":"","repo":"spinclass","branch":"mellow-mango"}` +
		`]`
	if string(data) != want {
		t.Errorf("marshal mismatch\n got: %s\nwant: %s", data, want)
	}
}

func TestListRowsClosedIncludesAbandoned(t *testing.T) {
	rows := ListRows(listRowFixtures(t), true)

	if len(rows) != 3 {
		t.Fatalf("rows: got %d, want 3", len(rows))
	}
	last := rows[2]
	if last.ID != "gone" || last.State != StateAbandoned || last.Description != "externally removed" {
		t.Errorf("abandoned row: %+v", last)
	}
}

// TestListRowsCarriesKind verifies the implicit-session marker survives
// the wire shape: a KindImplicit state yields Kind=="implicit" on its
// ListRow, while a worktree state (empty Kind) yields Kind=="" and its
// JSON omits the "kind" key entirely (omitempty preserves the legacy
// worktree-row shape).
func TestListRowsCarriesKind(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "spinclass")
	checkout := filepath.Join(base, "checkout")
	wt := filepath.Join(repo, ".worktrees", "mellow-mango")
	for _, dir := range []string{checkout, wt} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	states := []State{
		{
			Kind:         KindImplicit,
			SessionState: StateInactive,
			RepoPath:     checkout,
			WorktreePath: checkout,
			Branch:       "master",
			SessionKey:   "checkout/master",
		},
		{
			SessionState: StateInactive,
			RepoPath:     repo,
			WorktreePath: wt,
			Branch:       "mellow-mango",
			SessionKey:   "spinclass/mellow-mango",
		},
	}

	rows := ListRows(states, false)
	if len(rows) != 2 {
		t.Fatalf("rows: got %d, want 2", len(rows))
	}
	if rows[0].Kind != KindImplicit {
		t.Errorf("implicit row Kind = %q, want %q", rows[0].Kind, KindImplicit)
	}
	if rows[1].Kind != "" {
		t.Errorf("worktree row Kind = %q, want empty", rows[1].Kind)
	}

	// The worktree row's JSON must not carry a "kind" key (omitempty).
	data, err := json.Marshal(rows[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "kind") {
		t.Errorf("worktree row JSON unexpectedly contains \"kind\": %s", data)
	}
}

// TestListRowsCarriesSpawnedBy verifies the spawn lineage hint survives
// the wire shape (FDR 0006): a state with SpawnedBy yields the driver key
// on its ListRow, while an ordinary state yields "" and its JSON omits the
// "spawned_by" key entirely (omitempty preserves the legacy row shape).
func TestListRowsCarriesSpawnedBy(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "spinclass")
	spawned := filepath.Join(repo, ".worktrees", "spawned-walnut")
	plain := filepath.Join(repo, ".worktrees", "plain-pine")
	for _, dir := range []string{spawned, plain} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	states := []State{
		{
			SessionState: StateInactive,
			RepoPath:     repo,
			WorktreePath: spawned,
			Branch:       "spawned-walnut",
			SessionKey:   "spinclass/spawned-walnut",
			SpawnedBy:    "spinclass/bright-cedar",
		},
		{
			SessionState: StateInactive,
			RepoPath:     repo,
			WorktreePath: plain,
			Branch:       "plain-pine",
			SessionKey:   "spinclass/plain-pine",
		},
	}

	rows := ListRows(states, false)
	if len(rows) != 2 {
		t.Fatalf("rows: got %d, want 2", len(rows))
	}
	if rows[0].SpawnedBy != "spinclass/bright-cedar" {
		t.Errorf("spawned row SpawnedBy = %q, want %q", rows[0].SpawnedBy, "spinclass/bright-cedar")
	}
	if rows[1].SpawnedBy != "" {
		t.Errorf("plain row SpawnedBy = %q, want empty", rows[1].SpawnedBy)
	}

	// The plain row's JSON must not carry a "spawned_by" key (omitempty).
	data, err := json.Marshal(rows[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "spawned_by") {
		t.Errorf("plain row JSON unexpectedly contains \"spawned_by\": %s", data)
	}
}

// TestListRowsEmptyMarshalsToArray guards against `null` output: an
// empty session list must serialize as [] for remote consumers.
func TestListRowsEmptyMarshalsToArray(t *testing.T) {
	data, err := json.Marshal(ListRows(nil, false))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[]" {
		t.Errorf("empty marshal: got %s, want []", data)
	}
}
