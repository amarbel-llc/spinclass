// Package setupfingerprint computes a stable hash of everything that
// determines a worktree's spinclass setup — the merged sweatfile config, the
// spinclass binary version/commit, and the build-time-pinned tool paths. The
// hash is recorded in session state when setup is applied; a later mismatch
// against a freshly-computed fingerprint flags the worktree as stale (its
// installed setup drifted from current config/binary/pins) so `sc rebuild`
// (or resume auto-rebuild) can refresh it. See the rebuild/staleness design.
//
// What this DOESN'T catch: external-tool *version* drift where the config
// value is unchanged but the tool behind it (e.g. conformist on PATH) changed.
// That needs probing each tool and is out of scope.
package setupfingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"code.linenisgreat.com/spinclass/internal/embeds"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// Scheme is the fingerprint scheme version. Bump it whenever the SET of hashed
// inputs (below) changes, so every existing worktree's stored fingerprint
// reads as a scheme mismatch (an expected one-time mass rebuild) rather than
// silently colliding across the change.
const Scheme = 1

// Compute returns the setup fingerprint for the given merged sweatfile config
// plus the current binary's version/commit and pinned tool paths (read from
// internal/embeds). The hash is stable across struct-field reordering — the
// config is canonicalized via a sorted-key JSON round-trip — and changes when
// any setup-affecting input changes. The returned scheme is always Scheme;
// callers store it alongside the hash so IsStale can detect scheme changes.
func Compute(merged sweatfile.Sweatfile) (hash string, scheme int) {
	h := sha256.New()
	// Domain-separated, newline-delimited inputs. The config JSON is compacted
	// (no embedded newlines) so the final field is unambiguous.
	_, _ = fmt.Fprintf(h, "scheme\x00%d\n", Scheme)
	_, _ = fmt.Fprintf(h, "version\x00%s\n", embeds.Version())
	_, _ = fmt.Fprintf(h, "commit\x00%s\n", embeds.Commit())
	_, _ = fmt.Fprintf(h, "direnv\x00%s\n", embeds.DirenvBin())
	_, _ = fmt.Fprintf(h, "madder\x00%s\n", embeds.MadderBin())
	_, _ = fmt.Fprintf(h, "dodder\x00%s\n", embeds.DodderBin())
	_, _ = h.Write([]byte("config\x00"))
	_, _ = h.Write(canonicalJSON(merged))
	return hex.EncodeToString(h.Sum(nil)), Scheme
}

// canonicalJSON marshals v to JSON, round-trips through a generic value so all
// object keys are sorted (encoding/json sorts map keys on marshal) — making the
// output independent of struct field declaration order — and returns the
// re-marshaled bytes. The config holds only strings/bools/arrays/maps (no
// floats), so a sorted-key round-trip is sufficient canonicalization; full
// RFC-8785 JCS (and its dependency) is unnecessary. Marshal errors degrade to a
// deterministic error sentinel rather than panicking (a config that can't
// marshal would equally break everywhere else).
func canonicalJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte("ERR-marshal:" + err.Error())
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return raw
	}
	canon, err := json.Marshal(generic)
	if err != nil {
		return raw
	}
	return canon
}

// IsStale reports whether a worktree whose recorded fingerprint/scheme differ
// from the freshly-computed values needs a rebuild, with a human-readable
// reason. An empty recordedHash (a worktree created before staleness tracking)
// is stale.
func IsStale(recordedHash string, recordedScheme int, merged sweatfile.Sweatfile) (stale bool, reason string) {
	if recordedHash == "" {
		return true, "no recorded setup fingerprint (worktree predates staleness tracking)"
	}
	current, scheme := Compute(merged)
	if recordedScheme != scheme {
		return true, fmt.Sprintf("fingerprint scheme changed (%d → %d)", recordedScheme, scheme)
	}
	if recordedHash != current {
		return true, "worktree setup config, spinclass binary, or pinned tools changed since last apply"
	}
	return false, ""
}
