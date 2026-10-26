package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/0/embeds"
	"code.linenisgreat.com/spinclass/internal/0/madder"
	"code.linenisgreat.com/spinclass/internal/alfa/attestationindex"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/attestation"
	"code.linenisgreat.com/spinclass/internal/delta/merge"
	"code.linenisgreat.com/spinclass/internal/servelog"
)

// writeMadderBlobFn stores data in the per-user madder store (initialising it
// on first use) and returns the blob id ("" when madder is not pinned); a seam
// for tests.
var writeMadderBlobFn = func(data []byte) (string, error) {
	bin := embeds.MadderBin()
	if err := madder.InitUserStore(bin); err != nil {
		return "", err
	}
	w, finish, err := madder.WriteUserStore(bin)
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

// attestationBlob publishes the attestation tk claimed as a blob in the
// per-user madder store (madder.UserStoreID) and returns its URI, for
// post-merge as SPINCLASS_ATTESTATION_BLOB (#373). The stored copy is consumed
// when the merge lands, so this is the record that outlives it, and it
// outlives the session worktree too. "" when tk holds nothing or the write
// fails: like every madder write, a failure is logged and never fails the
// gate (#349).
func attestationBlob(tk attestation.Ticket) string {
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
	id, err := writeMadderBlobFn(data)
	if err != nil || id == "" {
		if err != nil {
			servelog.Errorf("attestation blob: %v", err)
		}
		return ""
	}
	return blobURIPrefix + id
}

const blobURIPrefix = "madder://blobs/"

// attestationOnLanded returns the PostMergeOptions.OnLanded callback that
// indexes the blob once the merge has landed, or nil when no blob was written.
// An index failure is logged and never fails the merge.
func attestationOnLanded(blobURI string, tk attestation.Ticket, repoPath, sessionKey string) func(string) {
	if blobURI == "" || tk.Claimed == nil {
		return nil
	}
	return func(landedSha string) {
		err := attestationindex.Write(attestationindex.Entry{
			ClaimID:    tk.ClaimID,
			Store:      madder.UserStoreID,
			Digest:     strings.TrimPrefix(blobURI, blobURIPrefix),
			Repo:       repoPath,
			SessionKey: sessionKey,
			HeadSha:    tk.Claimed.HeadSha,
			LandedSha:  landedSha,
			RecordedAt: time.Now().UTC(),
		})
		if err != nil {
			servelog.Errorf("attestation index: %v", err)
		}
	}
}

// applyAttestationBlob publishes tk's attestation and wires its URI, store and
// landed-index callback into pm.
func applyAttestationBlob(pm *merge.PostMergeOptions, tk attestation.Ticket, gs gatedSession) {
	pm.AttestationBlob = attestationBlob(tk)
	if pm.AttestationBlob == "" {
		return
	}
	pm.AttestationStore = madder.UserStoreID
	pm.OnLanded = attestationOnLanded(pm.AttestationBlob, tk, gs.repoPath, filepath.Base(gs.repoPath)+"/"+gs.branch)
}

// runAttestation prints the index entry for claimID and returns the exit code.
func runAttestation(stdout, stderr io.Writer, claimID string) int {
	e, err := attestationindex.Read(claimID)
	switch {
	case errors.Is(err, attestationindex.ErrNotFound):
		fmt.Fprintf(stderr, "attestation: no attestation recorded for claim id %q\n", claimID) //nolint:errcheck // status carries failure
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "attestation: %v\n", err) //nolint:errcheck // status carries failure
		return 1
	}
	data, _ := json.MarshalIndent(e, "", "  ")
	if _, err := fmt.Fprintln(stdout, string(data)); err != nil {
		return 1
	}
	return 0
}

func attestationCommand() *command.Command {
	return &command.Command{
		Name: "attestation",
		Description: command.Description{
			Short: "Print the recorded attestation for a claim id",
			Long: "Print the index entry (JSON) for a landed pre-merge attestation claim: claim_id, store, digest, repo, session_key, head_sha, landed_sha, recorded_at. " +
				"The blob itself lives in the per-user madder store named by `store` and is fetched with madder by `digest`. Read-only; CLI only. Exits non-zero when no entry exists.",
		},
		Params: []command.Param{
			{Name: "claim-id", Type: command.String, Description: "The attestation claim id", Required: true},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				ClaimID string `json:"claim-id"`
			}
			_ = json.Unmarshal(args, &p)
			if code := runAttestation(os.Stdout, os.Stderr, p.ClaimID); code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
}
