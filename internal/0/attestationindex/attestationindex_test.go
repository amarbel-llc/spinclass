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
