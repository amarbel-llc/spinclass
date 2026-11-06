package clean

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/attestationindex"
)

// writeIndexEntry records one attestation index entry aged as given.
func writeIndexEntry(t *testing.T, id string, recordedAt time.Time) {
	t.Helper()
	if err := attestationindex.Write(attestationindex.Entry{
		ClaimID: id, Store: "spinclass", Digest: "blake2b256-" + id,
		Repo: "/r", SessionKey: "r/b", HeadSha: "h", LandedSha: "l",
		RecordedAt: recordedAt,
	}); err != nil {
		t.Fatal(err)
	}
}

// With [madder].retain set in the repo's sweatfile, sc clean expires the index
// entries older than the duration and keeps the rest; the plan names the step
// (spinclass#427).
func TestCleanExpiresAttestationIndexEntriesPastRetain(t *testing.T) {
	_, repoDir := setupRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(repoDir, "sweatfile"), []byte("[madder]\nretain = \"720h\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeIndexEntry(t, "old", time.Now().Add(-40*24*time.Hour))
	writeIndexEntry(t, "fresh", time.Now().Add(-time.Hour))

	out := captureRun(t, repoDir, false, false, true, "tap")

	if !strings.Contains(out, "attestation index entries") {
		t.Errorf("TAP output never names the expiry step:\n%s", out)
	}
	if _, err := attestationindex.Read("old"); !errors.Is(err, attestationindex.ErrNotFound) {
		t.Errorf("old entry survived the sweep: %v", err)
	}
	if _, err := attestationindex.Read("fresh"); err != nil {
		t.Errorf("fresh entry was expired: %v", err)
	}
}

// A dry run plans the expiry and removes nothing.
func TestCleanDryRunKeepsAttestationIndexEntries(t *testing.T) {
	_, repoDir := setupRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(repoDir, "sweatfile"), []byte("[madder]\nretain = \"720h\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeIndexEntry(t, "old", time.Now().Add(-40*24*time.Hour))

	out := captureRun(t, repoDir, false, true, false, "tap")

	if !strings.Contains(out, "attestation index entries") {
		t.Errorf("dry-run TAP never plans the expiry:\n%s", out)
	}
	if _, err := attestationindex.Read("old"); err != nil {
		t.Errorf("dry run removed the entry: %v", err)
	}
}

// With no [madder].retain anywhere, the default keeps every entry forever and
// the plan says nothing about them.
func TestCleanKeepsAttestationIndexEntriesByDefault(t *testing.T) {
	_, repoDir := setupRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeIndexEntry(t, "ancient", time.Now().Add(-10*365*24*time.Hour))

	out := captureRun(t, repoDir, false, false, true, "tap")

	if strings.Contains(out, "attestation index") {
		t.Errorf("no retention is set, yet the plan mentions the index:\n%s", out)
	}
	if _, err := attestationindex.Read("ancient"); err != nil {
		t.Errorf("entry expired with no retention set: %v", err)
	}
}
