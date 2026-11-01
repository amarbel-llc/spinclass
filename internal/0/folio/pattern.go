// Package folio holds the pure grammar of the sweatfile [folio] stanza
// (spinclass#438, docs/plans/2026-10-27-folio-stanza-design.md section 2):
// the path patterns a policy is written in. It resolves nothing: the anchors
// are names, and the caller supplies their roots when it evaluates.
package folio

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Anchor is the root a pattern is relative to.
type Anchor string

const (
	// AnchorPwd is the session worktree (the hook's CWD).
	AnchorPwd Anchor = "pwd"
	// AnchorHome is the HOME of the process that evaluates.
	AnchorHome Anchor = "home"
	// AnchorEnv is the value of the environment variable named in Arg; empty
	// or unset, the pattern matches nothing.
	AnchorEnv Anchor = "env"
	// AnchorNixStore is /nix/store.
	AnchorNixStore Anchor = "nix-store"
	// AnchorSpinclass is one of the spinclass roots named in Arg: worktree,
	// repo, worktrees, children, siblings.
	AnchorSpinclass Anchor = "spinclass"
)

// SpinclassRoots are the roots AnchorSpinclass accepts. worktree is the
// caller's own worktree; repo the main checkout; worktrees the repo's
// .worktrees/; children the worktrees of sessions the caller holds an accepted
// handle on; siblings the parent of the main checkout minus the main checkout.
var SpinclassRoots = []string{"worktree", "repo", "worktrees", "children", "siblings"}

// Pattern is one parsed stanza entry. Segments is the path below the anchor's
// root, a directory-boundary prefix: it matches that path and everything under
// it. A segment "*" matches exactly one path segment. Raw is the entry as
// written.
type Pattern struct {
	Anchor   Anchor
	Arg      string
	Segments []string
	Raw      string
}

// String returns the entry as written.
func (p Pattern) String() string { return p.Raw }

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParsePattern parses one entry: <anchor>:<path>, with env:<NAME>:<path> and
// spinclass:<root>[/<path>]. The path is segments joined by "/", none empty,
// none "." or "..", none containing "~" or "*" except a whole-segment "*", with
// an optional trailing "/**" that only spells out the default prefix match.
func ParsePattern(raw string) (Pattern, error) {
	anchor, rest, ok := strings.Cut(raw, ":")
	if !ok || anchor == "" {
		return Pattern{}, fmt.Errorf("%q: an entry is <anchor>:<path>, with anchor one of pwd, home, env, nix-store, spinclass", raw)
	}
	p := Pattern{Anchor: Anchor(anchor), Raw: raw}
	var path string
	switch p.Anchor {
	case AnchorPwd, AnchorHome, AnchorNixStore:
		path = rest
	case AnchorEnv:
		name, after, ok := strings.Cut(rest, ":")
		if !ok || !envName.MatchString(name) {
			return Pattern{}, fmt.Errorf("%q: env entries are env:<VARIABLE>:<path>, with a variable name of letters, digits and underscores", raw)
		}
		p.Arg = name
		path = after
	case AnchorSpinclass:
		root, after, _ := strings.Cut(rest, "/")
		if !contains(SpinclassRoots, root) {
			return Pattern{}, fmt.Errorf("%q: spinclass entries are spinclass:<root>[/<path>], with root one of %s", raw, strings.Join(SpinclassRoots, ", "))
		}
		p.Arg = root
		path = after
		if strings.HasSuffix(rest, "/") && after == "" {
			return Pattern{}, fmt.Errorf("%q: a trailing slash says nothing; write /** or leave it off", raw)
		}
	default:
		return Pattern{}, fmt.Errorf("%q: unknown anchor %q; one of pwd, home, env, nix-store, spinclass", raw, anchor)
	}
	segs, err := parsePath(path)
	if err != nil {
		return Pattern{}, fmt.Errorf("%q: %w", raw, err)
	}
	p.Segments = segs
	return p, nil
}

func parsePath(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if strings.HasPrefix(path, "/") {
		return nil, errors.New("the path is relative to the anchor, so it must not start with a slash")
	}
	path, _ = strings.CutSuffix(path, "/**")
	if path == "" {
		return nil, nil
	}
	if strings.HasSuffix(path, "/") {
		return nil, errors.New("a trailing slash says nothing; write /** or leave it off")
	}
	segs := strings.Split(path, "/")
	for _, s := range segs {
		switch {
		case s == "":
			return nil, errors.New("empty path segment")
		case s == "." || s == "..":
			return nil, fmt.Errorf("%q is not allowed in a pattern", s)
		case s == "**":
			return nil, errors.New("** is only allowed at the end")
		case strings.Contains(s, "~"):
			return nil, errors.New("~ is not expanded in a pattern; use the home anchor instead")
		case strings.Contains(s, "*") && s != "*":
			return nil, errors.New("* must be a whole segment")
		}
	}
	return segs, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
