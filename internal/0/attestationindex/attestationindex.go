// Package attestationindex is the durable lookup from a claim id to the
// madder blob holding its attestation: one JSON file per landed claim under
// $XDG_STATE_HOME/spinclass/attestations/ (docs/plans/2026-10-26-durable-
// madder-store.md). Entries are kept forever by default; only an opt-in
// retention sweep (Prune, run by `sc clean` under [madder].retain,
// spinclass#427) removes any, and it removes index files alone, never blobs.
package attestationindex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// Stale lists the entries whose RecordedAt is before cutoff, oldest first: what
// a retention sweep would remove (spinclass#427). An index file that does not
// parse is skipped, never counted, so a damaged entry is not swept by accident;
// a missing index dir is simply empty.
func Stale(cutoff time.Time) ([]Entry, error) {
	dirEntries, err := os.ReadDir(Dir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range dirEntries {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".tmp-") {
			continue
		}
		e, err := Read(strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue
		}
		if e.RecordedAt.Before(cutoff) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RecordedAt.Before(out[j].RecordedAt) })
	return out, nil
}

// Prune removes the entries Stale(cutoff) names and returns their claim ids,
// oldest first. Only the index file goes: the blob it pointed at stays in the
// madder store, which has no delete primitive (madder's lane, spinclass#427).
// The first removal error is returned with the ids removed before it.
func Prune(cutoff time.Time) (removed []string, err error) {
	stale, err := Stale(cutoff)
	if err != nil {
		return nil, err
	}
	for _, e := range stale {
		if rerr := os.Remove(filepath.Join(Dir(), e.ClaimID+".json")); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return removed, rerr
		}
		removed = append(removed, e.ClaimID)
	}
	return removed, nil
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
