package main

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/attestation"
)

func stubAttestationBlobWrite(t *testing.T, id string, err error) *[]byte {
	t.Helper()
	var written []byte
	orig := writeMadderBlobFn
	writeMadderBlobFn = func(_ string, data []byte) (string, error) {
		written = data
		return id, err
	}
	t.Cleanup(func() { writeMadderBlobFn = orig })
	return &written
}

// #373: a claimed attestation is published as a madder blob holding the
// ticket and every skill entry, and its URI is what post-merge gets.
func TestAttestationBlobPublishesTheClaimedAttestation(t *testing.T) {
	written := stubAttestationBlobWrite(t, "blake2b256-abc", nil)
	tk := attestation.Ticket{HeadSha: "h1", ClaimID: "c1", Claimed: &session.PreMergeAttestation{
		RecordedAt: time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC),
		HeadSha:    "h1",
		Skills:     []session.AttestedSkill{{Name: "review", Used: true, Reasoning: "looked"}},
	}}

	if got := attestationBlob("/wt", tk); got != "madder://blobs/blake2b256-abc" {
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
	if got := attestationBlob("/wt", attestation.Ticket{}); got != "" {
		t.Errorf("zero ticket gave %q", got)
	}
	stubAttestationBlobWrite(t, "", errors.New("madder unavailable"))
	tk := attestation.Ticket{ClaimID: "c1", Claimed: &session.PreMergeAttestation{}}
	if got := attestationBlob("/wt", tk); got != "" {
		t.Errorf("madder failure gave %q", got)
	}
}
