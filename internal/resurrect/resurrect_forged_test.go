package resurrect

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// A tombstone is a copy of a record the closed worker could write. One whose
// Branch climbs out of <repo>/.worktrees must not make resurrect create a
// worktree outside it.
func TestRunRefusesATombstoneWhoseBranchEscapesTheWorktreesDir(t *testing.T) {
	repoPath, wtPath := setupClosedSession(t, "feature-x")

	index := filepath.Join(os.Getenv("XDG_STATE_HOME"), "spinclass", "index")
	name := func(wt string) string {
		h := sha256.Sum256([]byte(filepath.Clean(wt)))
		return filepath.Join(index, fmt.Sprintf("%x.json", h[:8]))
	}
	raw, err := os.ReadFile(name(wtPath))
	if err != nil {
		t.Fatal(err)
	}
	var tomb map[string]any
	if err := json.Unmarshal(raw, &tomb); err != nil {
		t.Fatal(err)
	}
	escaped := filepath.Join(repoPath, "escaped")
	tomb["branch"] = "../escaped"
	tomb["worktree_path"] = escaped
	tomb["session_key"] = "repo/../escaped"
	forged, err := json.Marshal(tomb)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name(escaped), forged, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Run(io.Discard, "repo/../escaped", "", "tap"); err == nil {
		t.Error("resurrect accepted a tombstone whose branch contains ..")
	}
	if _, err := os.Stat(escaped); err == nil {
		t.Errorf("a worktree was created outside <repo>/.worktrees at %s", escaped)
	}
}
