package sweatfileio

import "code.linenisgreat.com/spinclass/internal/0/sweatfile"

// TrustedUnattendedPermissionMode returns the [session-entry]
// unattended-permission-mode honoured for the repo: a scalar, the last layer
// above the repo that sets a non-empty value wins, "" when none does. It
// decides how loosely an unconfined detached worker may run, so a value an
// agent can write into the repo's own sweatfile must not choose it; the paths
// of the layers that set the key and may not are returned in untrusted so
// callers can surface them. The value is returned unvalidated (see
// sweatfile.ValidUnattendedPermissionMode). Hierarchy.Merged never carries the
// key.
func TrustedUnattendedPermissionMode(h sweatfile.Hierarchy, home, repoRoot string) (mode string, untrusted []string) {
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.SessionEntry == nil || src.File.SessionEntry.UnattendedPermissionMode == nil ||
			*src.File.SessionEntry.UnattendedPermissionMode == "" {
			continue
		}
		if LayerAboveRepo(src.Path, home, repoRoot) {
			mode = *src.File.SessionEntry.UnattendedPermissionMode
		} else {
			untrusted = append(untrusted, src.Path)
		}
	}
	return mode, untrusted
}
