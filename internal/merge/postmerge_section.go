package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/landing"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// The post-merge section is everything a sweatfile says about what runs on the
// frontend after a landing: the legacy [hooks].post-merge string, the phase cap,
// the disable flag and every active [[post-merge]] target. A merge that would
// change it is refused before landing unless an operator confirmed the new
// section's digest at a terminal (--accept-post-merge); agents cannot.

// PostMergeSection is the canonical, comparable form of a sweatfile's
// post-merge configuration. Field order is the JSON order and so part of the
// digest definition: do not reorder.
type PostMergeSection struct {
	LegacyHook *string                  `json:"legacy_hook"`
	Timeout    *string                  `json:"timeout"`
	Disabled   bool                     `json:"disabled"`
	Targets    []PostMergeSectionTarget `json:"targets"`
}

// PostMergeSectionTarget is one active [[post-merge]] target, all its fields.
type PostMergeSectionTarget struct {
	Name    string  `json:"name"`
	Command string  `json:"command"`
	Verify  *string `json:"verify"`
}

// PostMergeSectionOf extracts the section from a merged sweatfile. Removal
// sentinels (name-only targets) are dropped: they run nothing.
func PostMergeSectionOf(sf sweatfile.Sweatfile) PostMergeSection {
	s := PostMergeSection{
		LegacyHook: sf.PostMergeHookCommand(),
		Disabled:   sf.PostMergeDisabled(),
		Targets:    []PostMergeSectionTarget{},
	}
	if sf.Hooks != nil {
		s.Timeout = sf.Hooks.PostMergeTimeout
	}
	for _, t := range sf.ActivePostMergeTargets() {
		s.Targets = append(s.Targets, PostMergeSectionTarget{Name: t.Name, Command: t.Command, Verify: t.Verify})
	}
	return s
}

// Canonical is the JSON serialization the digest is taken over.
func (s PostMergeSection) Canonical() []byte {
	if s.Targets == nil {
		s.Targets = []PostMergeSectionTarget{}
	}
	b, err := json.Marshal(s)
	if err != nil {
		// Only strings, bools and nil pointers: cannot fail.
		panic(fmt.Sprintf("post-merge section marshal: %v", err))
	}
	return b
}

// Digest is "sha256:<hex>" of Canonical.
func (s PostMergeSection) Digest() string {
	sum := sha256.Sum256(s.Canonical())
	return "sha256:" + hex.EncodeToString(sum[:])
}

func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ChangesFrom is one line saying what this section changes relative to tip,
// e.g. "targets added: x; removed: y; changed: z; legacy hook changed".
func (s PostMergeSection) ChangesFrom(tip PostMergeSection) string {
	byName := func(ts []PostMergeSectionTarget) map[string]PostMergeSectionTarget {
		m := make(map[string]PostMergeSectionTarget, len(ts))
		for _, t := range ts {
			m[t.Name] = t
		}
		return m
	}
	old, cur := byName(tip.Targets), byName(s.Targets)
	var added, removed, changed []string
	for name, t := range cur {
		o, ok := old[name]
		switch {
		case !ok:
			added = append(added, name)
		case o.Command != t.Command || !strPtrEqual(o.Verify, t.Verify):
			changed = append(changed, name)
		}
	}
	for name := range old {
		if _, ok := cur[name]; !ok {
			removed = append(removed, name)
		}
	}
	var parts []string
	for _, g := range []struct {
		label string
		names []string
	}{{"added", added}, {"removed", removed}, {"changed", changed}} {
		if len(g.names) > 0 {
			sort.Strings(g.names)
			parts = append(parts, g.label+": "+strings.Join(g.names, ", "))
		}
	}
	if len(parts) > 0 {
		parts[0] = "targets " + parts[0]
	}
	if !strPtrEqual(s.LegacyHook, tip.LegacyHook) {
		parts = append(parts, "legacy hook changed")
	}
	if !strPtrEqual(s.Timeout, tip.Timeout) {
		parts = append(parts, "timeout changed")
	}
	if s.Disabled != tip.Disabled {
		parts = append(parts, "disable-post-merge changed")
	}
	if len(parts) == 0 {
		return "no change"
	}
	return strings.Join(parts, "; ")
}

// Describe renders the section for a human to review in full.
func (s PostMergeSection) Describe() string {
	indent := func(v string) string { return strings.ReplaceAll(v, "\n", "\n      ") }
	opt := func(p *string, none string) string {
		if p == nil {
			return none
		}
		return indent(*p)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "legacy hook ([hooks].post-merge): %s\n", opt(s.LegacyHook, "(none)"))
	fmt.Fprintf(&b, "timeout ([hooks].post-merge-timeout): %s\n", opt(s.Timeout, "(default)"))
	fmt.Fprintf(&b, "disable-post-merge: %t\n", s.Disabled)
	if len(s.Targets) == 0 {
		b.WriteString("targets: (none)\n")
	}
	for _, t := range s.Targets {
		fmt.Fprintf(&b, "target %s:\n    command: %s\n    verify:  %s\n", t.Name, indent(t.Command), opt(t.Verify, "(none)"))
	}
	return b.String()
}

// PostMergeSectionAt loads the section the sweatfile hierarchy yields with the
// repo layer committed at sha.
func PostMergeSectionAt(repoPath, sha string) (PostMergeSection, error) {
	home, _ := os.UserHomeDir()
	if home == "" {
		return PostMergeSection{}, fmt.Errorf("home directory unresolvable")
	}
	h, err := loadCommitHierarchy(home, repoPath, sha)
	if err != nil {
		return PostMergeSection{}, err
	}
	return PostMergeSectionOf(h.Merged), nil
}

// ComparePostMergeSections is the dry run behind `sc merge --show-post-merge`:
// the section on the landing target's tip (freshly fetched for a gitSync merge)
// against the section at mergedSha.
func ComparePostMergeSections(ctx context.Context, repoPath, wtPath, defaultBranch, mergedSha string, gitSync bool) (tip, merged PostMergeSection, err error) {
	target := landing.ForMerge(repoPath, defaultBranch, gitSync)
	if !target.IsSelf() {
		if out, fErr := target.Fetch(ctx, wtPath); fErr != nil {
			return tip, merged, fmt.Errorf("fetch %s: %w\n%s", target.Label(), fErr, out)
		}
	}
	if tip, err = PostMergeSectionAt(repoPath, target.Ref()); err != nil {
		return tip, merged, fmt.Errorf("post-merge section at %s: %w", target.Label(), err)
	}
	if merged, err = PostMergeSectionAt(repoPath, mergedSha); err != nil {
		return tip, merged, fmt.Errorf("post-merge section at %s: %w", shortSha(mergedSha), err)
	}
	return tip, merged, nil
}

// requirePostMergeSectionAccepted refuses, before anything lands, a merge whose
// post-merge section at landingSha differs from the one on the landing target's
// tip (tipRef) unless accept equals the digest of the new section. An unreadable
// section at landingSha passes: the post-merge phase skips it with a warning
// and runs nothing. An unreadable tip counts as an empty section. announce
// emits the ok point for an accepted change (the authoritative check only).
func requirePostMergeSectionAccepted(ts *crap.TestStream, branch, repoPath, tipRef, landingSha, accept string, announce bool) error {
	merged, err := PostMergeSectionAt(repoPath, landingSha)
	if err != nil {
		return nil
	}
	tip, tipErr := PostMergeSectionAt(repoPath, tipRef)
	if tipErr != nil {
		tip = PostMergeSectionOf(sweatfile.Sweatfile{})
	}
	tipDigest, mergedDigest := tip.Digest(), merged.Digest()
	if tipDigest == mergedDigest {
		return nil
	}
	label := "post-merge section " + branch
	if accept == mergedDigest {
		if announce {
			ts.Ok(fmt.Sprintf("%s: changed post-merge section accepted (%s)", label, mergedDigest))
		}
		return nil
	}
	var msg strings.Builder
	msg.WriteString("this merge changes the post-merge section, which runs commands on the frontend after landing; nothing landed\n")
	fmt.Fprintf(&msg, "  default branch tip: %s\n", tipDigest)
	fmt.Fprintf(&msg, "  this merge (%s): %s\n", shortSha(landingSha), mergedDigest)
	if accept != "" {
		fmt.Fprintf(&msg, "  the given digest %s does not match this merge's section digest\n", accept)
	}
	fmt.Fprintf(&msg, "  change: %s\n", merged.ChangesFrom(tip))
	msg.WriteString("an operator must review and accept it at a terminal (agents cannot):\n")
	fmt.Fprintf(&msg, "  sc merge --show-post-merge %s\n", branch)
	fmt.Fprintf(&msg, "  sc merge --accept-post-merge %s %s", mergedDigest, branch)
	return failStep(ts, label, fmt.Errorf("%s", msg.String()), "")
}
