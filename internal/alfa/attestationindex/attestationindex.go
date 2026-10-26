// Package attestationindex is the durable lookup from a claim id to the
// madder blob holding its attestation: one JSON file per landed claim under
// $XDG_STATE_HOME/spinclass/attestations/ (docs/plans/2026-10-26-durable-
// madder-store.md). Entries are kept forever; nothing here deletes them.
package attestationindex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Entry is one index file.
type Entry struct {
	ClaimID    string    `json:"claim_id"`
	Store      string    `json:"store"`
	Digest     string    `json:"digest"`
	Repo       string    `json:"repo"`
	SessionKey string    `json:"session_key"`
	HeadSha    string    `json:"head_sha"`
	LandedSha  string    `json:"landed_sha"`
	RecordedAt time.Time `json:"recorded_at"`
}

// ErrNotFound means no entry exists for the claim id.
var ErrNotFound = errors.New("no attestation index entry")

// Dir is $XDG_STATE_HOME/spinclass/attestations, defaulting the base to
// $HOME/.local/state.
func Dir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "spinclass", "attestations")
}

// ValidClaimID rejects anything that is not a single, plain path segment.
func ValidClaimID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return fmt.Errorf("invalid claim id %q: must be a single path segment", id)
	}
	return nil
}

// Write records e atomically (temp file + rename).
func Write(e Entry) error {
	if err := ValidClaimID(e.ClaimID); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+e.ClaimID+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, e.ClaimID+".json")); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// Read loads the entry for claimID; ErrNotFound when there is none.
func Read(claimID string) (Entry, error) {
	var e Entry
	if err := ValidClaimID(claimID); err != nil {
		return e, err
	}
	data, err := os.ReadFile(filepath.Join(Dir(), claimID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return e, fmt.Errorf("attestation index entry %s: %w", claimID, err)
	}
	return e, nil
}
