package sweatfile

import (
	"fmt"
	"strings"
	"time"
)

// TEMPORARY (spinclass#384 stopgap, docs/plans/2026-11-12-spawn-allowlist-stopgap.md):
// parsing of [[permissions.spawn-allow]] entries. Delete with the allow-list
// when ajour plus profiles enforce who may spawn unattended workers.

// DefaultSpawnAllowMaxWorkers is the live-worker cap of an entry that sets none.
const DefaultSpawnAllowMaxWorkers = 12

// SpawnAllowEntry is a SpawnAllow after validation. Problem is non-empty for an
// entry that must be ignored (fail closed); the other fields are then not to be
// trusted.
type SpawnAllowEntry struct {
	// Principal is the bare per-instance key an entry naming a principal lists.
	// A qualified <key>@<host> is accepted for future cross-host handles
	// (spinclass#478 Q7, interim rule) and reduced to its key: a per-instance
	// key identifies one session, so a key that is not a live local session
	// simply never matches a local caller.
	Principal string
	// Host is the host part of a qualified principal, kept for the journal.
	Host string
	// Session is the <repo>/<branch> key of an entry naming a session.
	Session    string
	MaxWorkers int
	// Expires is the zero Time when the entry never expires.
	Expires time.Time
	Note    string
	Problem string
}

// ParseSpawnAllow validates one entry. Exactly one of principal and session is
// required.
func ParseSpawnAllow(e SpawnAllow) SpawnAllowEntry {
	out := SpawnAllowEntry{MaxWorkers: DefaultSpawnAllowMaxWorkers, Note: e.Note}
	invalid := func(format string, a ...any) SpawnAllowEntry {
		out.Problem = fmt.Sprintf(format, a...)
		return out
	}
	switch {
	case e.Principal != "" && e.Session != "":
		return invalid("names both principal and session; give exactly one")
	case e.Principal == "" && e.Session == "":
		return invalid("names neither principal nor session; give exactly one")
	case e.Principal != "":
		if strings.ContainsAny(e.Principal, " \t\r\n/\\") {
			return invalid("principal %q is not a principal key", e.Principal)
		}
		key, host, qualified := strings.Cut(e.Principal, "@")
		if key == "" || (qualified && (host == "" || strings.Contains(host, "@"))) {
			return invalid("principal %q is not <key> or <key>@<host>", e.Principal)
		}
		out.Principal, out.Host = key, host
	default:
		if !strings.Contains(e.Session, "/") || strings.ContainsAny(e.Session, " \t\r\n") {
			return invalid("session %q is not a <repo>/<branch> session key", e.Session)
		}
		out.Session = e.Session
	}
	if e.MaxWorkers != nil {
		if *e.MaxWorkers < 1 {
			return invalid("max-workers is %d: it must be at least 1", *e.MaxWorkers)
		}
		out.MaxWorkers = *e.MaxWorkers
	}
	if e.Expires != "" {
		t, err := time.Parse(time.RFC3339, e.Expires)
		if err != nil {
			return invalid("expires %q is not an RFC 3339 instant", e.Expires)
		}
		out.Expires = t.UTC()
	}
	return out
}

// Expired reports whether the entry's expiry has passed at now.
func (e SpawnAllowEntry) Expired(now time.Time) bool {
	return !e.Expires.IsZero() && !now.Before(e.Expires)
}
