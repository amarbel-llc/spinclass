package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/attestationindex"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/attestation"
)

func stubAttestationBlobWrite(t *testing.T, id string, err error) *[]byte {
	t.Helper()
	var written []byte
	orig := writeMadderBlobFn
	writeMadderBlobFn = func(data []byte) (string, error) {
		written = data
		return id, err
	}
	t.Cleanup(func() { writeMadderBlobFn = orig })
	return &written
}

func claimedTicket() attestation.Ticket {
	return attestation.Ticket{HeadSha: "h1", ClaimID: "c1", Claimed: &session.PreMergeAttestation{
		RecordedAt: time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC),
		HeadSha:    "h1",
		Skills:     []session.AttestedSkill{{Name: "review", Used: true, Reasoning: "looked"}},
	}}
}

// #373: a claimed attestation is published as a madder blob holding the
// ticket and every skill entry, and its URI is what post-merge gets.
func TestAttestationBlobPublishesTheClaimedAttestation(t *testing.T) {
	written := stubAttestationBlobWrite(t, "blake2b256-abc", nil)

	if got := attestationBlob(claimedTicket()); got != "madder://blobs/blake2b256-abc" {
		t.Fatalf("attestationBlob = %q", got)
	}
	var rec struct {
		ClaimID    string                  `json:"claim_id"`
		HeadSha    string                  `json:"head_sha"`
		RecordedAt time.Time               `json:"recorded_at"`
		Skills     []session.AttestedSkill `json:"skills"`
	}
	if err := json.Unmarshal(*written, &rec); err != nil {
		t.Fatalf("blob is not JSON: %v: %s", err, *written)
	}
	if rec.ClaimID != "c1" || rec.HeadSha != "h1" || rec.RecordedAt.IsZero() || len(rec.Skills) != 1 || rec.Skills[0].Reasoning != "looked" {
		t.Errorf("blob = %+v", rec)
	}
}

// No claim means no blob; a madder failure means no blob and never an error.
func TestAttestationBlobIsEmptyWithoutAClaimOrOnMadderFailure(t *testing.T) {
	stubAttestationBlobWrite(t, "x", nil)
	if got := attestationBlob(attestation.Ticket{}); got != "" {
		t.Errorf("zero ticket gave %q", got)
	}
	stubAttestationBlobWrite(t, "", errors.New("madder unavailable"))
	tk := attestation.Ticket{ClaimID: "c1", Claimed: &session.PreMergeAttestation{}}
	if got := attestationBlob(tk); got != "" {
		t.Errorf("madder failure gave %q", got)
	}
}

// The index entry records the durable blob once the merge has landed.
func TestAttestationOnLandedWritesTheIndexEntry(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cb := attestationOnLanded("madder://blobs/blake2b256-abc", claimedTicket(), "/r/repo", "repo/feature")
	if cb == nil {
		t.Fatal("no callback for a written blob")
	}
	cb("landed1")

	e, err := attestationindex.Read("c1")
	if err != nil {
		t.Fatal(err)
	}
	if e.ClaimID != "c1" || e.Store != "spinclass" || e.Digest != "blake2b256-abc" || e.Repo != "/r/repo" ||
		e.SessionKey != "repo/feature" || e.HeadSha != "h1" || e.LandedSha != "landed1" || e.RecordedAt.IsZero() {
		t.Errorf("entry = %+v", e)
	}
}

func TestAttestationOnLandedIsNilWithoutABlob(t *testing.T) {
	if cb := attestationOnLanded("", claimedTicket(), "/r", "r/b"); cb != nil {
		t.Error("callback for an empty blob")
	}
}

// An unwritable index is logged, never a panic or a failed merge.
func TestAttestationOnLandedSwallowsIndexFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", blocker)
	attestationOnLanded("madder://blobs/x", claimedTicket(), "/r", "r/b")("l")
}

func TestRunAttestationPrintsTheEntryAndFailsWhenMissing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := runAttestation(&out, &errb, "c1"); code == 0 || !strings.Contains(errb.String(), "no attestation") {
		t.Fatalf("missing: code=%d stderr=%q", code, errb.String())
	}

	attestationOnLanded("madder://blobs/blake2b256-abc", claimedTicket(), "/r/repo", "repo/feature")("landed1")
	out.Reset()
	errb.Reset()
	if code := runAttestation(&out, &errb, "c1"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	var e attestationindex.Entry
	if err := json.Unmarshal(out.Bytes(), &e); err != nil || e.LandedSha != "landed1" || e.Digest != "blake2b256-abc" {
		t.Errorf("stdout = %q (%v)", out.String(), err)
	}

	out.Reset()
	errb.Reset()
	if code := runAttestation(&out, &errb, "../c1"); code == 0 || !strings.Contains(errb.String(), "claim id") {
		t.Errorf("traversal: code=%d stderr=%q", code, errb.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout on failure: %q", out.String())
	}
}
