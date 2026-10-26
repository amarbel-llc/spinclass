package main

import (
	"encoding/json"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/embeds"
	"code.linenisgreat.com/spinclass/internal/0/madder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/attestation"
	"code.linenisgreat.com/spinclass/internal/servelog"
)

// writeMadderBlobFn stores data in wtPath's per-worktree madder store and
// returns the blob id ("" when madder is not pinned); a seam for tests.
var writeMadderBlobFn = func(wtPath string, data []byte) (string, error) {
	w, finish, err := madder.Write(wtPath, embeds.MadderBin())
	if err != nil {
		return "", err
	}
	_, _ = w.Write(data)
	_ = w.Close()
	return finish()
}

// attestationRecord is the published form of a claimed attestation: the
// ticket that held it and every skill entry it made.
type attestationRecord struct {
	ClaimID    string                  `json:"claim_id"`
	HeadSha    string                  `json:"head_sha,omitempty"`
	RecordedAt time.Time               `json:"recorded_at"`
	Skills     []session.AttestedSkill `json:"skills"`
}

// attestationBlob publishes the attestation tk claimed as a madder blob in
// the session worktree's store and returns its URI, for post-merge as
// SPINCLASS_ATTESTATION_BLOB (#373). The stored copy is consumed when the
// merge lands, so this is the record that outlives it, for as long as the
// worktree (whose store holds it) does. "" when tk holds nothing or the write
// fails: like every madder write, a failure is logged and never fails the
// gate (#349).
func attestationBlob(wtPath string, tk attestation.Ticket) string {
	if tk.Claimed == nil {
		return ""
	}
	data, err := json.Marshal(attestationRecord{
		ClaimID:    tk.ClaimID,
		HeadSha:    tk.Claimed.HeadSha,
		RecordedAt: tk.Claimed.RecordedAt,
		Skills:     tk.Claimed.Skills,
	})
	if err != nil {
		servelog.Errorf("attestation blob: %v", err)
		return ""
	}
	id, err := writeMadderBlobFn(wtPath, data)
	if err != nil || id == "" {
		if err != nil {
			servelog.Errorf("attestation blob: %v", err)
		}
		return ""
	}
	return "madder://blobs/" + id
}
