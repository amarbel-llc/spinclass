package clown

import (
	"time"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

// Presence is one clown instance's presence record (RFC-0013 §3.3), as
// ringmaster's host service keeps it (RFC-0020 §6.4). spinclass consumes the
// listing read-only for session liveness and the `sc list` 1-to-many view;
// Decoration is the spinclass session key (clown's CLOWN_GROUP_ID =
// ${SPINCLASS_SESSION_ID}).
type Presence struct {
	SessionKey  string `json:"sessionKey"`
	ChannelID   string `json:"channelId"`
	Decoration  string `json:"decoration"`
	Description string `json:"description"`
	LastSeen    string `json:"lastSeen"`
}

// ReadPresence returns the live clown presence records from ringmaster's host
// service (jobwake.ListPresence) — the clown→spinclass consumer half of the
// awareness seam. The service owns staleness (RFC-0020 §6.4), so every record
// it lists is live. Best-effort: an unreachable service (bare spinclass, no
// clown, no ringmaster service) yields zero records, never an error — liveness
// degrades to dead, never false-alive.
func ReadPresence(now time.Time) []Presence {
	listed, err := jobwake.ListPresence(now)
	if err != nil {
		return nil
	}
	out := make([]Presence, 0, len(listed))
	for _, p := range listed {
		out = append(out, Presence{
			SessionKey:  p.SessionKey,
			ChannelID:   p.ChannelID,
			Decoration:  p.Decoration,
			Description: p.Description,
			LastSeen:    p.LastSeen,
		})
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
