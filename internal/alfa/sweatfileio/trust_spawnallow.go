package sweatfileio

import (
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// TEMPORARY (spinclass#384 stopgap, docs/plans/2026-11-12-spawn-allowlist-stopgap.md):
// delete with the allow-list when ajour plus profiles enforce this.

// TrustedSpawnAllow returns the [[permissions.spawn-allow]] entries honoured for
// the repo: those of layers above the repo, appended across layers in order.
// Layers that may not set the list (the repo and worktree files, which the
// session worker writes) are never honoured; their paths are returned in
// untrusted so callers can surface them. An entry that does not validate is
// returned with Problem set and must be ignored by the caller (fail closed).
// Hierarchy.Merged never carries the table.
func TrustedSpawnAllow(h sweatfile.Hierarchy, home, repoRoot string) (entries []sweatfile.SpawnAllowEntry, untrusted []string) {
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.Permissions == nil || len(src.File.Permissions.SpawnAllow) == 0 {
			continue
		}
		if !LayerAboveRepo(src.Path, home, repoRoot) {
			untrusted = append(untrusted, src.Path)
			continue
		}
		for _, e := range src.File.Permissions.SpawnAllow {
			entries = append(entries, sweatfile.ParseSpawnAllow(e))
		}
	}
	return entries, untrusted
}
