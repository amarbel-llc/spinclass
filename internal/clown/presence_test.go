package clown

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/testfs"
)

func writePresence(t *testing.T, dir string, p Presence) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(dir, p.ChannelID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadPresenceDropsStale(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "clown", "presence")
	now := time.Now()

	writePresence(t, dir, Presence{SessionKey: "uuid-a", ChannelID: "aaaa", Decoration: "repo/feat", LastSeen: now.Format(time.RFC3339Nano)})
	writePresence(t, dir, Presence{SessionKey: "uuid-b", ChannelID: "bbbb", Decoration: "repo/feat", LastSeen: now.Add(-5 * time.Minute).Format(time.RFC3339Nano)})
	writePresence(t, dir, Presence{SessionKey: "uuid-c", ChannelID: "cccc", Decoration: "repo/other", LastSeen: now.Format(time.RFC3339Nano)})

	got := ReadPresence(now)
	if len(got) != 2 {
		t.Fatalf("ReadPresence = %d records, want 2 (stale dropped): %+v", len(got), got)
	}
}

// TestReadPresenceAcceptsRFC3339WithoutFraction guards the bats fixture path:
// a lastSeen with no fractional seconds (what `date -u +%Y-%m-%dT%H:%M:%SZ`
// emits) must still parse against RFC3339Nano and count as live.
func TestReadPresenceAcceptsRFC3339WithoutFraction(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "clown", "presence")
	now := time.Now()
	writePresence(t, dir, Presence{ChannelID: "nf", Decoration: "repo/feat", LastSeen: now.UTC().Format("2006-01-02T15:04:05Z")})
	if got := ReadPresence(now); len(got) != 1 {
		t.Errorf("ReadPresence = %d, want 1 (no-fraction RFC3339 must parse)", len(got))
	}
}

func TestReadPresenceMissingDirIsEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // no clown/presence subdir
	if got := ReadPresence(time.Now()); len(got) != 0 {
		t.Errorf("ReadPresence with no dir = %+v, want empty", got)
	}
}

func TestReadPresenceSkipsUnparseable(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "clown", "presence")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// Garbage file + a record with a bad timestamp — both dropped, no error.
	testfs.MustWriteFile(t, filepath.Join(dir, "garbage.json"), []byte("not json"), 0o600)
	writePresence(t, dir, Presence{ChannelID: "bad", Decoration: "repo/x", LastSeen: "not-a-time"})
	writePresence(t, dir, Presence{ChannelID: "ok", Decoration: "repo/x", LastSeen: now.Format(time.RFC3339Nano)})

	if got := ReadPresence(now); len(got) != 1 {
		t.Errorf("ReadPresence = %d, want 1 (garbage + bad-timestamp dropped)", len(got))
	}
}

func TestPresenceByDecorationGroupsAndSkipsUngrouped(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "clown", "presence")
	now := time.Now()
	writePresence(t, dir, Presence{ChannelID: "a", Decoration: "repo/feat", LastSeen: now.Format(time.RFC3339Nano)})
	writePresence(t, dir, Presence{ChannelID: "b", Decoration: "repo/feat", LastSeen: now.Format(time.RFC3339Nano)})
	writePresence(t, dir, Presence{ChannelID: "c", Decoration: "", LastSeen: now.Format(time.RFC3339Nano)}) // ungrouped

	byKey := PresenceByDecoration(now)
	if len(byKey) != 1 {
		t.Errorf("groups = %d, want 1 (ungrouped skipped)", len(byKey))
	}
	if len(byKey["repo/feat"]) != 2 {
		t.Errorf("repo/feat = %d clowns, want 2", len(byKey["repo/feat"]))
	}
}
