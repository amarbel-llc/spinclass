package sweatfileio

import "code.linenisgreat.com/spinclass/internal/0/sweatfile"

// FolioPolicy is the [folio] stanza as honoured for a repo (spinclass#438):
// the raw pattern strings, not yet compiled. Configured is whether any layer
// above the repo set anything in the stanza; a policy that is not configured
// means the session has no broker policy, and serve must then not listen, so
// moxy keeps its static floor (today's behaviour).
type FolioPolicy struct {
	Read       []string
	Write      []string
	Deny       []string
	Unmatched  string
	Configured bool
}

// TrustedFolio returns the [folio] stanza honoured for the repo. The allow
// lists (read, write) and unmatched are read only from layers above the repo:
// allows append across those layers and an explicit [] in a nearer one clears
// the ones above; unmatched is a scalar, the last trusted layer wins. Denies
// only narrow, so they are honoured from every layer and appended, trusted
// layers first in hierarchy order and then the others; only a trusted layer's
// [] clears the denies above it. Layers that may not set an allow or unmatched
// but do are returned in untrusted. Hierarchy.Merged never carries the table.
func TrustedFolio(h sweatfile.Hierarchy, home, repoRoot string) (policy FolioPolicy, untrusted []string) {
	var lowerDenies []string
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found || src.File.Folio == nil {
			continue
		}
		f := src.File.Folio
		if !LayerAboveRepo(src.Path, home, repoRoot) {
			if f.Read != nil || f.Write != nil || f.Unmatched != nil {
				untrusted = append(untrusted, src.Path)
			}
			lowerDenies = append(lowerDenies, f.Deny...)
			continue
		}
		policy.Configured = true
		policy.Read = appendOrClear(policy.Read, f.Read)
		policy.Write = appendOrClear(policy.Write, f.Write)
		policy.Deny = appendOrClear(policy.Deny, f.Deny)
		if f.Unmatched != nil {
			policy.Unmatched = *f.Unmatched
		}
	}
	policy.Deny = append(policy.Deny, lowerDenies...)
	return policy, untrusted
}

// appendOrClear is the trusted-layer list rule: nil inherits, [] clears,
// non-empty appends.
func appendOrClear(acc, layer []string) []string {
	if layer == nil {
		return acc
	}
	if len(layer) == 0 {
		return []string{}
	}
	return append(acc, layer...)
}
