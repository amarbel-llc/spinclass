package sweatfileio

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// LayerAboveRepo reports whether the sweatfile layer at layerPath sits above
// the repo: it is the global sweatfile, or its directory is a strict ancestor
// of repoRoot. Lexical dir is compared with the cleaned root and canonical
// dir with the canonical root, never crosswise. Label layers (non-absolute
// paths such as a committed-blob label) are never above (#335).
//
// The global exemption is a path-equality check, so the global sweatfile is
// trusted even if $HOME lies inside the repo. "/" is never a layer dir
// (chainAncestors never emits it).
func LayerAboveRepo(layerPath, home, repoRoot string) bool {
	if layerPath == filepath.Join(home, ".config", "spinclass", "sweatfile") {
		return true
	}
	if !filepath.IsAbs(layerPath) {
		return false
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return false
	}
	dir := filepath.Dir(layerPath)
	return strictAncestor(dir, absRoot) ||
		strictAncestor(canonicalDir(dir), canonicalDir(absRoot))
}

func strictAncestor(dir, root string) bool {
	rel, err := filepath.Rel(dir, root)
	if err != nil || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// TrustedEnforceProfileRights returns the [profile-rights].enforce switch
// honoured for the repo. The switch is STICKY-ON: it is true if ANY layer above
// the repo says true, and a false anywhere never clears a true. That makes it
// monotone, which matters because repoRoot is a path the caller (possibly an
// agent) chose: an agent that relocates or symlinks its target repo under a
// directory it controls, or outside the operator's enforcing tree, can only
// change which ancestor layers are consulted, and a layer that says false has no
// power to undo one that says true. Callers must still OR the result over every
// context they can (the global sweatfile is in every hierarchy; the spawn driver
// adds its own repo's), so the path chosen cannot shrink the set of honoured
// trues below the global one.
//
// Off when unset. Layers that may not set it (repo, its realpath twin,
// worktree, labels) are never honoured, so an agent editing its own sweatfile
// cannot turn enforcement off; the paths of those that set the key are returned
// in untrusted so callers can surface them. Hierarchy.Merged never carries the
// key.
//
// What remains: a same-uid agent can edit the global sweatfile itself (FDR 0032
// D17). This closes the sanctioned-tool-surface path, not that one.
func TrustedEnforceProfileRights(h sweatfile.Hierarchy, home, repoRoot string) (enforce bool, untrusted []string) {
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.ProfileRights == nil || src.File.ProfileRights.Enforce == nil {
			continue
		}
		if LayerAboveRepo(src.Path, home, repoRoot) {
			enforce = enforce || *src.File.ProfileRights.Enforce
		} else {
			untrusted = append(untrusted, src.Path)
		}
	}
	return enforce, untrusted
}

// TrustedTransitionTTL returns the [profile-rights].transition-ttl honoured for
// the repo (#407): the last layer above the repo that sets it wins, and
// sweatfile.DefaultTransitionTTL applies when none does. A trusted value that
// does not parse is an error, not a silent default, because it is the
// operator's own setting. Layers that may not set the key are never honoured;
// their paths are returned in untrusted.
func TrustedTransitionTTL(h sweatfile.Hierarchy, home, repoRoot string) (ttl time.Duration, untrusted []string, err error) {
	ttl = sweatfile.DefaultTransitionTTL
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.ProfileRights == nil || src.File.ProfileRights.TransitionTTL == nil {
			continue
		}
		if !LayerAboveRepo(src.Path, home, repoRoot) {
			untrusted = append(untrusted, src.Path)
			continue
		}
		d, perr := sweatfile.ParseTransitionTTL(*src.File.ProfileRights.TransitionTTL)
		if perr != nil {
			return 0, untrusted, fmt.Errorf("%s: %w", src.Path, perr)
		}
		ttl = d
	}
	return ttl, untrusted, nil
}

// ConfinementPolicy is the resolved [confinement] switches for one context.
type ConfinementPolicy struct {
	// Enabled is the feature switch: confinement is available at all.
	Enabled bool
	// DefaultSpawn is whether a spawn is confined unless it says otherwise.
	// Resolved: true when no trusted layer sets it. Meaningless unless Enabled.
	DefaultSpawn bool
}

// DefaultConfines reports whether a spawn that makes no request is confined.
func (p ConfinementPolicy) DefaultConfines() bool { return p.Enabled && p.DefaultSpawn }

// TrustedConfinement returns the [confinement] settings honoured for the repo.
//
// enabled and default-spawn are scalars: the last trusted layer that sets each
// wins, so a nearer layer can turn either off (unlike the sticky-on
// profile-rights switch). launch-unit is the deprecated alias for both true: it
// stays sticky-on across trusted layers, and supplies each key's value only
// when no trusted layer sets that key itself. An unset default-spawn is true.
// egress is the last trusted layer that sets egress-allow (nil inherits, []
// clears); with none set it is nil, which the launch spec leaves out so clown's
// own default list applies. Layers that may not set the table are never
// honoured; their paths are returned in untrusted. Hierarchy.Merged never
// carries the table.
func TrustedConfinement(h sweatfile.Hierarchy, home, repoRoot string) (policy ConfinementPolicy, egress []string, untrusted []string) {
	var enabled, defaultSpawn *bool
	legacy := false
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found || src.File.Confinement == nil {
			continue
		}
		c := src.File.Confinement
		if c.Enabled == nil && c.DefaultSpawn == nil && c.LaunchUnit == nil && c.EgressAllow == nil {
			continue
		}
		if !LayerAboveRepo(src.Path, home, repoRoot) {
			untrusted = append(untrusted, src.Path)
			continue
		}
		if c.Enabled != nil {
			enabled = c.Enabled
		}
		if c.DefaultSpawn != nil {
			defaultSpawn = c.DefaultSpawn
		}
		if c.LaunchUnit != nil {
			legacy = legacy || *c.LaunchUnit
		}
		if c.EgressAllow != nil {
			egress = c.EgressAllow
		}
	}
	policy.Enabled = legacy
	if enabled != nil {
		policy.Enabled = *enabled
	}
	policy.DefaultSpawn = true
	if defaultSpawn != nil {
		policy.DefaultSpawn = *defaultSpawn
	}
	return policy, egress, untrusted
}

// TrustedDenySweatfileEdits returns the [permissions].deny-sweatfile-edits
// switch honoured for the repo: a scalar, the last layer above the repo that
// sets it wins, off when unset. Layers that may not set it are never honoured;
// their paths are returned in untrusted. Hierarchy.Merged never carries the
// table.
func TrustedDenySweatfileEdits(h sweatfile.Hierarchy, home, repoRoot string) (deny bool, untrusted []string) {
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.Permissions == nil || src.File.Permissions.DenySweatfileEdits == nil {
			continue
		}
		if LayerAboveRepo(src.Path, home, repoRoot) {
			deny = *src.File.Permissions.DenySweatfileEdits
		} else {
			untrusted = append(untrusted, src.Path)
		}
	}
	return deny, untrusted
}

// TrustedStoreIdentity returns the [store] identity honoured for the repo: each
// key is a scalar override across layers above the repo, the last layer that
// sets it wins, and the committer keys default to the author keys afterwards.
// Layers that may not set it (repo, its realpath twin, worktree) are never
// honoured; the paths of those that set any key are returned in untrusted.
// Hierarchy.Merged never carries the table.
func TrustedStoreIdentity(h sweatfile.Hierarchy, home, repoRoot string) (authorName, authorEmail, committerName, committerEmail string, untrusted []string) {
	trusted := &sweatfile.Store{}
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found || src.File.Store == nil {
			continue
		}
		s := src.File.Store
		if s.AuthorName == nil && s.AuthorEmail == nil && s.CommitterName == nil && s.CommitterEmail == nil {
			continue
		}
		if !LayerAboveRepo(src.Path, home, repoRoot) {
			untrusted = append(untrusted, src.Path)
			continue
		}
		if s.AuthorName != nil {
			trusted.AuthorName = s.AuthorName
		}
		if s.AuthorEmail != nil {
			trusted.AuthorEmail = s.AuthorEmail
		}
		if s.CommitterName != nil {
			trusted.CommitterName = s.CommitterName
		}
		if s.CommitterEmail != nil {
			trusted.CommitterEmail = s.CommitterEmail
		}
	}
	authorName, authorEmail, committerName, committerEmail = sweatfile.Sweatfile{Store: trusted}.StoreIdentity()
	return authorName, authorEmail, committerName, committerEmail, untrusted
}

// TrustedAllowSelfClose returns the [permissions].allow-self-close switch
// honoured for the repo (#389): a scalar, the last layer above the repo that
// sets it wins, off when unset. Layers that may not set it are never honoured;
// their paths are returned in untrusted. Hierarchy.Merged never carries the
// table.
func TrustedAllowSelfClose(h sweatfile.Hierarchy, home, repoRoot string) (allow bool, untrusted []string) {
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.Permissions == nil || src.File.Permissions.AllowSelfClose == nil {
			continue
		}
		if LayerAboveRepo(src.Path, home, repoRoot) {
			allow = *src.File.Permissions.AllowSelfClose
		} else {
			untrusted = append(untrusted, src.Path)
		}
	}
	return allow, untrusted
}

// TrustedSpawnProfile returns the [session-entry].spawn-profile default honoured
// for a CONFINED spawn into the repo (FDR 0035): a scalar, the last layer above
// the repo that sets a non-empty value wins, "" when none does. A confined
// worker's profile decides which tools it can reach, so a value an agent can
// write into the repo's own sweatfile must not choose it; the paths of the
// layers that set the key and may not are returned in untrusted so callers can
// surface them. An unconfined spawn keeps the merged read
// (Sweatfile.SessionSpawnProfile).
func TrustedSpawnProfile(h sweatfile.Hierarchy, home, repoRoot string) (profile string, untrusted []string) {
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.SessionEntry == nil || src.File.SessionEntry.SpawnProfile == "" {
			continue
		}
		if LayerAboveRepo(src.Path, home, repoRoot) {
			profile = src.File.SessionEntry.SpawnProfile
		} else {
			untrusted = append(untrusted, src.Path)
		}
	}
	return profile, untrusted
}

// TrustedRestartHandoff returns the [session-entry].restart argv honoured for
// the repo (#419): an argv, the last layer above the repo that sets it wins, an
// explicit [] clearing it, nil when none does. It is code spinclass execs, so a
// value an agent can write into the repo's own sweatfile must not choose it;
// the paths of the layers that set the key and may not are returned in
// untrusted. Hierarchy.Merged never carries the key.
func TrustedRestartHandoff(h sweatfile.Hierarchy, home, repoRoot string) (argv []string, untrusted []string) {
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.SessionEntry == nil || src.File.SessionEntry.Restart == nil {
			continue
		}
		if LayerAboveRepo(src.Path, home, repoRoot) {
			argv = src.File.SessionEntry.Restart
		} else {
			untrusted = append(untrusted, src.Path)
		}
	}
	if len(argv) == 0 {
		return nil, untrusted
	}
	return argv, untrusted
}

// TrustedURLResolver returns the [auth].url-resolver command honoured for the
// repo: scalar override across layers above the repo, last one wins, an
// explicit "" clearing it. The value is TrimSpaced, so whitespace-only also
// clears. Layers that may not set it (repo, its realpath twin, worktree) are
// never honoured; the paths of those that set the key are returned in
// untrusted so callers can surface them. Hierarchy.Merged never carries the
// key.
func TrustedURLResolver(h sweatfile.Hierarchy, home, repoRoot string) (command string, untrusted []string) {
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.Auth == nil || src.File.Auth.URLResolver == nil {
			continue
		}
		if LayerAboveRepo(src.Path, home, repoRoot) {
			command = strings.TrimSpace(*src.File.Auth.URLResolver)
		} else {
			untrusted = append(untrusted, src.Path)
		}
	}
	return command, untrusted
}
