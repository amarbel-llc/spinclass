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
	// HarnessConfigWritable is [confinement] harness-config-writable (#400):
	// false (the default) binds the worktree's .claude and .mcp.json read-only.
	HarnessConfigWritable bool
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
	var enabled, defaultSpawn, harnessWritable *bool
	legacy := false
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found || src.File.Confinement == nil {
			continue
		}
		c := src.File.Confinement
		if c.Enabled == nil && c.DefaultSpawn == nil && c.LaunchUnit == nil && c.EgressAllow == nil && c.HarnessConfigWritable == nil {
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
		if c.HarnessConfigWritable != nil {
			harnessWritable = c.HarnessConfigWritable
		}
	}
	policy.HarnessConfigWritable = harnessWritable != nil && *harnessWritable
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

// TrustedAllowMergeCommits returns the [hooks].allow-merge-commits switch
// honoured for the repo (#468): a scalar, the last layer above the repo that
// sets it wins, off when unset. Layers that may not set it (the repo and
// worktree files, which the session worker writes) are never honoured; their
// paths are returned in untrusted. A trusted value covers every repo under the
// layer that sets it. Hierarchy.Merged never carries the key.
func TrustedAllowMergeCommits(h sweatfile.Hierarchy, home, repoRoot string) (allow bool, untrusted []string) {
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.Hooks == nil || src.File.Hooks.AllowMergeCommits == nil {
			continue
		}
		if LayerAboveRepo(src.Path, home, repoRoot) {
			allow = *src.File.Hooks.AllowMergeCommits
		} else {
			untrusted = append(untrusted, src.Path)
		}
	}
	return allow, untrusted
}

// TrustedHostQueueRecordCosts returns the [host-queue].record-costs switch
// honoured for the repo (#424 step 0): a scalar, the last layer above the repo
// that sets it wins, on when unset. Host policy is the operator's, so layers
// that may not set it (the repo and worktree files) are never honoured; their
// paths are returned in untrusted. Hierarchy.Merged never carries the table.
func TrustedHostQueueRecordCosts(h sweatfile.Hierarchy, home, repoRoot string) (record bool, untrusted []string) {
	record = true
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.HostQueue == nil || src.File.HostQueue.RecordCosts == nil {
			continue
		}
		if LayerAboveRepo(src.Path, home, repoRoot) {
			record = *src.File.HostQueue.RecordCosts
		} else {
			untrusted = append(untrusted, src.Path)
		}
	}
	return record, untrusted
}

// TrustedRepairGenerated returns the [hooks].repair-generated mode honoured for
// the repo: a scalar, the last honoured layer wins, protect when none sets it.
// protect and fail only narrow what repair may do to generated files, so they
// are honoured from every layer; allow is honoured only from layers above the
// repo, and the paths of the layers whose allow was ignored are returned in
// untrusted. A value outside the three counts as protect (`sc validate`
// reports it). Hierarchy.Merged never carries the key.
func TrustedRepairGenerated(h sweatfile.Hierarchy, home, repoRoot string) (mode sweatfile.RepairGeneratedMode, untrusted []string) {
	mode = sweatfile.RepairGeneratedProtect
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.Hooks == nil || src.File.Hooks.RepairGenerated == nil {
			continue
		}
		m, _ := sweatfile.ParseRepairGenerated(*src.File.Hooks.RepairGenerated)
		if m == sweatfile.RepairGeneratedAllow && !LayerAboveRepo(src.Path, home, repoRoot) {
			untrusted = append(untrusted, src.Path)
			continue
		}
		mode = m
	}
	return mode, untrusted
}

// DefaultConfinedPreMerge is the gate a guarded merge runs when no trusted
// layer sets [hooks].confined-pre-merge (spinclass#449): the flake's own
// checks, evaluated purely, with a lock that must already be complete.
const DefaultConfinedPreMerge = "nix flake check --no-update-lock-file"

// ConfinedPreMergeGate is the resolved [hooks].confined-pre-merge: the command,
// the trusted layer it came from ("" for the default), and the layers whose
// value was ignored because they may not set it (the repo layer), for the
// caller to say so where the gate runs.
type ConfinedPreMergeGate struct {
	Command string
	Source  string
	Ignored []string
}

// TrustedConfinedPreMerge returns the [hooks].confined-pre-merge gate honoured
// for the repo (spinclass#449): a scalar, the last layer above the repo that
// sets it wins, an empty value in such a layer clears back to the default, and
// DefaultConfinedPreMerge applies when none sets it. The repo layer may not
// set it: a guarded merge exists because the worker controls the repo's tree,
// sweatfile included, so the gate it runs must come from a file the worker
// cannot write. Layers that may not set it are returned in untrusted.
func TrustedConfinedPreMerge(h sweatfile.Hierarchy, home, repoRoot string) (gate ConfinedPreMergeGate, untrusted []string) {
	gate.Command = DefaultConfinedPreMerge
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.Hooks == nil || src.File.Hooks.ConfinedPreMerge == nil {
			continue
		}
		if !LayerAboveRepo(src.Path, home, repoRoot) {
			untrusted = append(untrusted, src.Path)
			continue
		}
		if cmd := strings.TrimSpace(*src.File.Hooks.ConfinedPreMerge); cmd != "" {
			gate = ConfinedPreMergeGate{Command: cmd, Source: src.Path}
		} else {
			gate = ConfinedPreMergeGate{Command: DefaultConfinedPreMerge}
		}
	}
	gate.Ignored = untrusted
	return gate, untrusted
}

// DefaultHealthBusyBound is the busy bound when no trusted layer sets one
// (operator ruling, 2026-10-26).
const DefaultHealthBusyBound = 60 * time.Minute

// TrustedHealthBusyBound returns the [health].busy-bound honoured for the repo:
// a scalar, the last layer above the repo that sets a valid positive duration
// wins, DefaultHealthBusyBound otherwise. An unparseable or non-positive value
// is ignored (the previous value stands) so a typo cannot disable detection.
// Layers that may not set it are never honoured; their paths are returned in
// untrusted. Hierarchy.Merged never carries the table.
func TrustedHealthBusyBound(h sweatfile.Hierarchy, home, repoRoot string) (bound time.Duration, untrusted []string) {
	bound = DefaultHealthBusyBound
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.Health == nil || src.File.Health.BusyBound == nil {
			continue
		}
		if !LayerAboveRepo(src.Path, home, repoRoot) {
			untrusted = append(untrusted, src.Path)
			continue
		}
		if d, err := time.ParseDuration(strings.TrimSpace(*src.File.Health.BusyBound)); err == nil && d > 0 {
			bound = d
		}
	}
	return bound, untrusted
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
