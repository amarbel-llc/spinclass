package attestationindex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleEntry() Entry {
	return Entry{
		ClaimID:    "claim-1",
		Store:      "spinclass",
		Digest:     "blake2b256-abc",
		Repo:       "/r/repo",
		SessionKey: "repo/branch",
		HeadSha:    "h1",
		LandedSha:  "l1",
		RecordedAt: time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC),
	}
}

func TestWriteThenReadRoundTrips(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	if err := Write(sampleEntry()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "spinclass", "attestations", "claim-1.json")); err != nil {
		t.Fatalf("index file missing: %v", err)
	}
	got, err := Read("claim-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != sampleEntry() {
		t.Errorf("got %+v", got)
	}
}

func TestWriteUsesTheJSONFieldNames(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	if err := Write(sampleEntry()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(state, "spinclass", "attestations", "claim-1.json"))
	for _, key := range []string{`"claim_id"`, `"store"`, `"digest"`, `"repo"`, `"session_key"`, `"head_sha"`, `"landed_sha"`, `"recorded_at"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("index lacks %s: %s", key, raw)
		}
	}
}

func TestWriteIsAtomicLeavesNoTempFiles(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	for i := 0; i < 2; i++ {
		if err := Write(sampleEntry()); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(state, "spinclass", "attestations"))
	if len(ents) != 1 || ents[0].Name() != "claim-1.json" {
		t.Errorf("dir = %v", ents)
	}
}

func TestStateDirDefaultsToHomeLocalState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	if got, want := Dir(), filepath.Join(home, ".local", "state", "spinclass", "attestations"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

func TestReadMissingIsErrNotFound(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := Read("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// writeAged records an entry with the given claim id and age before now.
func writeAged(t *testing.T, id string, recordedAt time.Time) {
	t.Helper()
	e := sampleEntry()
	e.ClaimID = id
	e.RecordedAt = recordedAt
	if err := Write(e); err != nil {
		t.Fatal(err)
	}
}

// Stale names only the entries recorded before the cutoff, oldest first, and
// skips a file that is not a parseable entry rather than counting it
// (spinclass#427).
func TestStaleListsOnlyEntriesBeforeTheCutoffOldestFirst(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	writeAged(t, "old-b", now.Add(-40*24*time.Hour))
	writeAged(t, "old-a", now.Add(-50*24*time.Hour))
	writeAged(t, "fresh", now.Add(-1*time.Hour))
	dir := filepath.Join(state, "spinclass", "attestations")
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".tmp-old-a-123"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	stale, err := Stale(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range stale {
		ids = append(ids, e.ClaimID)
	}
	if want := []string{"old-a", "old-b"}; strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("Stale = %v, want %v", ids, want)
	}
}

// Prune removes exactly the stale index files, returns their ids oldest first,
// and leaves the fresh entry and the unparseable file in place.
func TestPruneRemovesOnlyStaleIndexFiles(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	writeAged(t, "old", now.Add(-400*24*time.Hour))
	writeAged(t, "fresh", now)
	dir := filepath.Join(state, "spinclass", "attestations")
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := Prune(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "old" {
		t.Errorf("Prune removed %v, want [old]", removed)
	}
	if _, err := Read("old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old entry still readable: %v", err)
	}
	if _, err := Read("fresh"); err != nil {
		t.Errorf("fresh entry gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "broken.json")); err != nil {
		t.Errorf("unparseable file was removed: %v", err)
	}
}

// With no index dir at all, a sweep finds nothing and removes nothing.
func TestStaleAndPruneOnAMissingDirAreEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if stale, err := Stale(time.Now()); err != nil || len(stale) != 0 {
		t.Errorf("Stale = %v, %v", stale, err)
	}
	if removed, err := Prune(time.Now()); err != nil || len(removed) != 0 {
		t.Errorf("Prune = %v, %v", removed, err)
	}
}

func TestClaimIDMustBeOneSafePathSegment(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, bad := range []string{"", ".", "..", "a/b", "../x", "a\\b", "a\x00b"} {
		if err := ValidClaimID(bad); err == nil {
			t.Errorf("ValidClaimID(%q) accepted", bad)
		}
		if _, err := Read(bad); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Read(%q) err = %v, want a validation error", bad, err)
		}
		e := sampleEntry()
		e.ClaimID = bad
		if err := Write(e); err == nil {
			t.Errorf("Write accepted claim id %q", bad)
		}
	}
}
