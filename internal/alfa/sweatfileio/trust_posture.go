package sweatfileio

import "code.linenisgreat.com/spinclass/internal/0/sweatfile"

// TrustedAllowPostureWidening returns the [session-entry] allow-posture-widening
// honoured for the repo (spinclass#342): a boolean, the last layer above the
// repo that sets it wins, false when none does. It decides whether a restart may
// move a session from unattended (dontAsk) to attended, so a value an agent can
// write into the repo's own sweatfile must not choose it; the paths of the
// layers that set the key and may not are returned in untrusted so callers can
// surface them. Hierarchy.Merged never carries the key.
func TrustedAllowPostureWidening(h sweatfile.Hierarchy, home, repoRoot string) (allowed bool, untrusted []string) {
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.SessionEntry == nil || src.File.SessionEntry.AllowPostureWidening == nil {
			continue
		}
		if LayerAboveRepo(src.Path, home, repoRoot) {
			allowed = *src.File.SessionEntry.AllowPostureWidening
		} else {
			untrusted = append(untrusted, src.Path)
		}
	}
	return allowed, untrusted
}
