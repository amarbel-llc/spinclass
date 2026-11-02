// Package folioeval evaluates a compiled [folio] policy (spinclass#438): given
// the roots the anchors expand to, an operation and an absolute path, it
// answers allow, ask or deny. It is pure over its inputs; building the roots
// from the session (worktrees, handles, environment) is roots.go.
//
// Rule (ruled 2026-11-01, "css selector-like"): the most specific matching
// entry wins. Specificity is the ordered pair (literal segments, wildcard
// segments) of the entry's path under its anchor, compared like a CSS
// specificity tuple: more literal segments wins; with equal literals, more
// `*` segments wins. On a full tie deny wins over allow. An allow counts only
// for its own operation. When no entry matches the policy's unmatched
// verdict applies. A path that cannot be resolved matches nothing. A blob
// resource is matched by the madder: anchor alone (EvaluateBlob).
package folioeval

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/folio"
	"code.linenisgreat.com/spinclass/internal/0/policybroker"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// Policy is a compiled stanza: every entry parsed, unmatched set.
type Policy struct {
	Read, Write, Deny []folio.Pattern
	Unmatched         string
}

// Compile parses every entry of the trusted policy and checks unmatched. Any
// defect refuses the whole policy: a stanza that is partly applied could
// drop a deny, which would widen it.
func Compile(p sweatfileio.FolioPolicy) (*Policy, error) {
	if !p.Configured {
		return nil, errors.New("[folio] is not configured in any layer above the repo")
	}
	out := &Policy{Unmatched: p.Unmatched}
	for _, list := range []struct {
		name    string
		entries []string
		dst     *[]folio.Pattern
	}{{"read", p.Read, &out.Read}, {"write", p.Write, &out.Write}, {"deny", p.Deny, &out.Deny}} {
		for _, e := range list.entries {
			pat, err := folio.ParsePattern(e)
			if err != nil {
				return nil, fmt.Errorf("[folio] %s: %w", list.name, err)
			}
			*list.dst = append(*list.dst, pat)
		}
	}
	switch p.Unmatched {
	case policybroker.VerdictAsk, policybroker.VerdictDeny:
	case "":
		return nil, errors.New("[folio] sets no `unmatched` in a trusted layer")
	default:
		return nil, fmt.Errorf("[folio] unmatched %q is not ask or deny", p.Unmatched)
	}
	return out, nil
}

// Roots is what the anchors expand to for one evaluation. Every path is
// absolute and symlink-resolved; an empty path means the anchor matches
// nothing. Env resolves env: entries; nil matches nothing.
type Roots struct {
	Pwd      string
	Home     string
	NixStore string
	Env      func(name string) string
	// Spinclass roots.
	Worktree  string
	Repo      string
	Worktrees string
	// Children are the worktrees of sessions the caller holds an accepted
	// handle on.
	Children []string
	// SiblingsParent is the parent of the main checkout; a sibling is any
	// directory under it other than SiblingsExclude (the main checkout).
	SiblingsParent, SiblingsExclude string
}

// Specificity is an entry's (literal, wildcard) segment counts under its
// anchor, compared in that order.
type Specificity struct{ Literals, Wildcards int }

// Beats reports whether s is strictly more specific than o.
func (s Specificity) Beats(o Specificity) bool {
	if s.Literals != o.Literals {
		return s.Literals > o.Literals
	}
	return s.Wildcards > o.Wildcards
}

// SpecificityOf is the pair for one pattern.
func SpecificityOf(p folio.Pattern) Specificity {
	var s Specificity
	for _, seg := range p.Segments {
		if seg == "*" {
			s.Wildcards++
		} else {
			s.Literals++
		}
	}
	return s
}

// match is one entry that matched a path.
type match struct {
	pattern     folio.Pattern
	specificity Specificity
}

// Evaluate answers op on path (absolute, already resolved by the caller's
// rule). The reason names the winning entry or the unmatched rule.
func (p *Policy) Evaluate(r Roots, op, path string) (verdict, reason string) {
	if !filepath.IsAbs(path) {
		return p.Unmatched, "unmatched: path is not absolute"
	}
	return p.decide(op, func(pat folio.Pattern) bool { return matchPattern(r, pat, path) })
}

// blobPrefix is the shape of a blob resource's URI (moxy RFC 0002): the
// digest follows, with no path after it.
const blobPrefix = "madder://blobs/"

// EvaluateBlob answers op on a blob resource (ruled 2026-11-02, option B):
// the madder: anchor matches every madder://blobs/<digest> URI and nothing
// else matches one, under the same tuple rule, so every madder: entry is
// (0,0) and one in deny beats one in an allow list. A URI not of that shape
// matches nothing.
func (p *Policy) EvaluateBlob(op, uri string) (verdict, reason string) {
	digest, ok := strings.CutPrefix(uri, blobPrefix)
	if !ok || digest == "" || strings.Contains(digest, "/") {
		return p.Unmatched, "unmatched: resource is not a " + blobPrefix + "<digest> URI"
	}
	return p.decide(op, func(pat folio.Pattern) bool { return pat.Anchor == folio.AnchorMadder })
}

// decide applies the rule over the entries matches admits: the most specific
// allow for op against the most specific deny, deny on a full tie.
func (p *Policy) decide(op string, matches func(folio.Pattern) bool) (verdict, reason string) {
	var allows []folio.Pattern
	switch op {
	case policybroker.OpRead:
		allows = p.Read
	case policybroker.OpWrite:
		allows = p.Write
	}
	bestAllow, okAllow := best(allows, matches)
	bestDeny, okDeny := best(p.Deny, matches)
	switch {
	case okDeny && (!okAllow || !bestAllow.specificity.Beats(bestDeny.specificity)):
		return policybroker.VerdictDeny, "deny " + bestDeny.pattern.String()
	case okAllow:
		return policybroker.VerdictAllow, op + " " + bestAllow.pattern.String()
	}
	return p.Unmatched, "unmatched"
}

func best(patterns []folio.Pattern, matches func(folio.Pattern) bool) (match, bool) {
	var top match
	found := false
	for _, pat := range patterns {
		if !matches(pat) {
			continue
		}
		spec := SpecificityOf(pat)
		if !found || spec.Beats(top.specificity) {
			top, found = match{pattern: pat, specificity: spec}, true
		}
	}
	return top, found
}

// matchPattern reports whether pat matches the file path under r. A madder:
// entry is for blobs and never matches a file.
func matchPattern(r Roots, pat folio.Pattern, path string) bool {
	segs := pat.Segments
	var roots []string
	switch pat.Anchor {
	case folio.AnchorMadder:
		return false
	case folio.AnchorPwd:
		roots = []string{r.Pwd}
	case folio.AnchorHome:
		roots = []string{r.Home}
	case folio.AnchorNixStore:
		roots = []string{r.NixStore}
	case folio.AnchorEnv:
		if r.Env != nil {
			roots = []string{r.Env(pat.Arg)}
		}
	case folio.AnchorSpinclass:
		switch pat.Arg {
		case "worktree":
			roots = []string{r.Worktree}
		case "repo":
			roots = []string{r.Repo}
		case "worktrees":
			roots = []string{r.Worktrees}
		case "children":
			roots = r.Children
		case "siblings":
			if r.SiblingsParent == "" {
				return false
			}
			roots = []string{r.SiblingsParent}
			// A sibling is one directory under the parent: an implicit `*`
			// that belongs to the anchor, not to the entry's specificity.
			segs = append([]string{"*"}, segs...)
		}
	}
	for _, root := range roots {
		if !matchUnder(root, segs, path) {
			continue
		}
		if pat.Anchor == folio.AnchorSpinclass && pat.Arg == "siblings" && r.SiblingsExclude != "" {
			rel, _ := filepath.Rel(root, path)
			first := strings.SplitN(rel, string(filepath.Separator), 2)[0]
			if filepath.Join(root, first) == r.SiblingsExclude {
				continue
			}
		}
		return true
	}
	return false
}

// matchUnder matches segs under root as a directory-boundary prefix of path.
func matchUnder(root string, segs []string, path string) bool {
	if root == "" || !filepath.IsAbs(root) {
		return false
	}
	root = filepath.Clean(root)
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(path, root), string(filepath.Separator))
	var relSegs []string
	if rel != "" {
		relSegs = strings.Split(rel, string(filepath.Separator))
	}
	if len(relSegs) < len(segs) {
		return false
	}
	for i, s := range segs {
		if s != "*" && relSegs[i] != s {
			return false
		}
	}
	return true
}
