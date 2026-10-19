package sweatfileio

import (
	"path/filepath"
	"slices"
	"strings"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
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

// TrustedConfinement returns the [confinement] settings honoured for the repo.
// launch is STICKY-ON across layers above the repo (same rule and rationale as
// TrustedEnforceProfileRights). egress is the last trusted layer that sets
// egress-allow (nil inherits, [] clears); with none set it is
// sweatfile.DefaultEgressAllow. Layers that may not set the table are never
// honoured; their paths are returned in untrusted. Hierarchy.Merged never
// carries the table.
func TrustedConfinement(h sweatfile.Hierarchy, home, repoRoot string) (launch bool, egress []string, untrusted []string) {
	var set bool
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found || src.File.Confinement == nil {
			continue
		}
		c := src.File.Confinement
		if c.LaunchUnit == nil && c.EgressAllow == nil {
			continue
		}
		if !LayerAboveRepo(src.Path, home, repoRoot) {
			untrusted = append(untrusted, src.Path)
			continue
		}
		if c.LaunchUnit != nil {
			launch = launch || *c.LaunchUnit
		}
		if c.EgressAllow != nil {
			egress, set = c.EgressAllow, true
		}
	}
	if !set {
		egress = slices.Clone(sweatfile.DefaultEgressAllow)
	}
	return launch, egress, untrusted
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
