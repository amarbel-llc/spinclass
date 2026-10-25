package clown

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// presenceStale mirrors clown's RFC-0014 §4.2 staleness window: a presence
// record not refreshed within this span is treated as dead. Liveness derived
// from presence MUST degrade a stale/missing record to dead, never false-alive.
const presenceStale = 2 * time.Minute

// Presence is one clown instance's presence record (clown RFC-0014 §4.1,
// mirroring clown's internal/jobwake/presence.go). spinclass consumes the index
// read-only for session liveness and the `sc list` 1-to-many view; Decoration
// is the spinclass session key (clown's CLOWN_GROUP_ID = ${SPINCLASS_SESSION_ID}).
type Presence struct {
	SessionKey  string `json:"sessionKey"`
	ChannelID   string `json:"channelId"`
	Decoration  string `json:"decoration"`
	Description string `json:"description"`
	LastSeen    string `json:"lastSeen"`
}

// presenceDir is clown's presence index directory:
// $XDG_STATE_HOME/clown/presence/ (XDG_STATE_HOME defaults to ~/.local/state
// per the basedir spec). Returns "" when no home can be resolved.
func presenceDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "clown", "presence")
}

// ReadPresence returns the live (non-stale) clown presence records — the
// clown→spinclass consumer half of the RFC-0014 awareness seam. It is read-only
// and best-effort: a missing directory (bare spinclass, or no clown) or an
// unreadable/unparseable record yields fewer/zero records, never an error.
// Records whose lastSeen is older than the staleness window, or unparseable,
// are dropped (degrade stale → dead, never false-alive).
func ReadPresence(now time.Time) []Presence {
	dir := presenceDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Presence
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var p Presence
		if json.Unmarshal(data, &p) != nil {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, p.LastSeen)
		if err != nil || now.Sub(t) > presenceStale {
			continue
		}
		out = append(out, p)
	}
	return out
}

// PresenceByDecoration groups the live presence records by their decoration
// (the spinclass session key). Records with an empty decoration (an ungrouped
// clown, not running under a spinclass session) are omitted.
func PresenceByDecoration(now time.Time) map[string][]Presence {
	byKey := map[string][]Presence{}
	for _, p := range ReadPresence(now) {
		if p.Decoration == "" {
			continue
		}
		byKey[p.Decoration] = append(byKey[p.Decoration], p)
	}
	return byKey
}
