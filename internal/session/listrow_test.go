package session

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		`{"id":"crisp-catalpa","session_key":"spinclass/crisp-catalpa","state":"active","description":"remote sessions","repo":"spinclass"},` +
		`{"id":"mellow-mango","session_key":"spinclass/mellow-mango","state":"inactive","description":"","repo":"spinclass"}` +
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
