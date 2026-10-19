// Package profilegrant implements the pure chain rules for profile grants
// (FDR 0034, "Authority"): the canonical record text, the narrowing relation
// between a grant and its parent, and the walk from a presented grant to a
// card-signed root. It does no I/O and no cryptography; signature checking is
// injected through Verifier.
package profilegrant

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	header       = "spinclass profile-grant v1"
	rightPrefix  = "spinclass:profile:"
	noValue      = "-"
	maxTextBytes = 4096 // piggy RFC 0012 operator-act text limit
	timeLayout   = "2006-01-02T15:04:05Z"
)

// Onward says whether the grantee may pass profile rights on.
type Onward string

const (
	OnwardNone  Onward = "none"
	OnwardGrant Onward = "grant"
)

// Grant is one profile-grant record body.
type Grant struct {
	Grantor  string
	Grantee  string
	Session  string            // "-" for a grantee that is a service, not a session
	Profiles map[string]string // profile name -> digest of its resolved definition
	Onward   Onward
	Expires  time.Time // UTC
	Nonce    string
	Prev     string // digest of the parent grant, "-" for a card root
}

// Rights returns the sorted rights derived from the profile set.
func (g Grant) Rights() []string {
	names := g.profileNames()
	rights := make([]string, len(names))
	for i, n := range names {
		rights[i] = rightPrefix + n
	}
	return rights
}

func (g Grant) profileNames() []string {
	names := make([]string, 0, len(g.Profiles))
	for n := range g.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// CanonicalText renders the record in the fixed line order a card signs.
func (g Grant) CanonicalText() string {
	var profiles []string
	for _, n := range g.profileNames() {
		profiles = append(profiles, n+"="+g.Profiles[n])
	}
	var b strings.Builder
	line := func(key, value string) { b.WriteString(key + ": " + value + "\n") }
	b.WriteString(header + "\n")
	line("grantor", g.Grantor)
	line("grantee", g.Grantee)
	line("session", g.Session)
	line("profiles", strings.Join(profiles, " "))
	line("rights", strings.Join(g.Rights(), " "))
	line("onward", string(g.Onward))
	line("expires", g.Expires.UTC().Format(timeLayout))
	line("nonce", g.Nonce)
	line("prev", g.Prev)
	return b.String()
}

var lineKeys = []string{
	"grantor", "grantee", "session", "profiles", "rights",
	"onward", "expires", "nonce", "prev",
}

// ParseCanonicalText is the strict inverse of CanonicalText: anything that is
// not exactly the canonical form is rejected, so a verifier never signs or
// trusts text it would render differently.
func ParseCanonicalText(s string) (Grant, error) {
	if len(s) > maxTextBytes {
		return Grant{}, fmt.Errorf("text is %d bytes, limit %d", len(s), maxTextBytes)
	}
	if !utf8.ValidString(s) {
		return Grant{}, errors.New("text is not valid UTF-8")
	}
	for _, r := range s {
		if r != '\n' && r != '\t' && (r < 0x20 || r == 0x7f) {
			return Grant{}, fmt.Errorf("control character %U not allowed", r)
		}
	}
	if !strings.HasSuffix(s, "\n") {
		return Grant{}, errors.New("text must end with a newline")
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if lines[0] != header {
		return Grant{}, fmt.Errorf("wrong header %q, want %q", lines[0], header)
	}
	body := lines[1:]
	if len(body) != len(lineKeys) {
		return Grant{}, fmt.Errorf("got %d body lines, want %d", len(body), len(lineKeys))
	}
	values := make(map[string]string, len(lineKeys))
	for i, key := range lineKeys {
		prefix := key + ": "
		if !strings.HasPrefix(body[i], prefix) {
			return Grant{}, fmt.Errorf("line %d: want %q line, got %q", i+2, key, body[i])
		}
		value := strings.TrimPrefix(body[i], prefix)
		if value == "" || value != strings.TrimSpace(value) {
			return Grant{}, fmt.Errorf("line %q: empty or space-padded value", key)
		}
		values[key] = value
	}

	g := Grant{
		Grantor: values["grantor"],
		Grantee: values["grantee"],
		Session: values["session"],
		Nonce:   values["nonce"],
		Prev:    values["prev"],
	}

	profiles, err := parseProfiles(values["profiles"])
	if err != nil {
		return Grant{}, err
	}
	g.Profiles = profiles
	if want := strings.Join(g.Rights(), " "); values["rights"] != want {
		return Grant{}, fmt.Errorf("rights %q do not match profiles, derived %q", values["rights"], want)
	}

	switch Onward(values["onward"]) {
	case OnwardNone, OnwardGrant:
		g.Onward = Onward(values["onward"])
	default:
		return Grant{}, fmt.Errorf("onward %q, want none or grant", values["onward"])
	}

	expires, err := time.Parse(timeLayout, values["expires"])
	if err != nil {
		return Grant{}, fmt.Errorf("expires: %w", err)
	}
	g.Expires = expires.UTC()
	return g, nil
}

func parseProfiles(value string) (map[string]string, error) {
	profiles := map[string]string{}
	previous := ""
	for _, field := range strings.Split(value, " ") {
		name, digest, ok := strings.Cut(field, "=")
		if !ok || name == "" || digest == "" {
			return nil, fmt.Errorf("profile entry %q, want name=digest", field)
		}
		if strings.ContainsAny(digest, "= \t") || strings.ContainsAny(name, "\t") {
			return nil, fmt.Errorf("profile entry %q contains whitespace or extra '='", field)
		}
		if name <= previous {
			return nil, fmt.Errorf("profile %q is duplicated or out of order", name)
		}
		previous = name
		profiles[name] = digest
	}
	return profiles, nil
}

// Holds reports whether the effective grant carries the profile.
func Holds(effective Grant, profile string) bool {
	_, ok := effective.Profiles[profile]
	return ok
}

// Narrows returns nil iff child is a valid narrowing of parent, whose digest
// is parentDigest. Narrowing is the only change a software key may sign;
// anything wider needs the card.
func Narrows(parent Grant, parentDigest string, child Grant) error {
	if child.Prev != parentDigest {
		return fmt.Errorf("child prev %q is not parent digest %q", child.Prev, parentDigest)
	}
	// Bind the chain by principal: only the parent's grantee may narrow it
	// onward. Without this, anyone who learned a parent digest could sign a
	// valid-looking child under it and borrow the grantee's rights.
	if child.Grantor != parent.Grantee {
		return fmt.Errorf("child grantor %q is not parent grantee %q", child.Grantor, parent.Grantee)
	}
	if child.Onward == OnwardGrant && parent.Onward != OnwardGrant {
		return fmt.Errorf("onward grant not permitted: parent holds %s", parent.Onward)
	}
	if child.Expires.After(parent.Expires) {
		return fmt.Errorf("child expiry after parent expiry (child %s, parent %s)",
			child.Expires.UTC().Format(timeLayout), parent.Expires.UTC().Format(timeLayout))
	}
	for _, name := range child.profileNames() {
		parentDigestOfProfile, held := parent.Profiles[name]
		if !held {
			return fmt.Errorf("profile %s not held by parent", name)
		}
		if childDigest := child.Profiles[name]; childDigest != parentDigestOfProfile {
			return fmt.Errorf("profile %s digest differs (parent %s, child %s)",
				name, parentDigestOfProfile, childDigest)
		}
	}
	return nil
}

// Verifier checks signatures; the real implementation lives with the piggy
// integration. Root grants carry a card signature, every other link a
// software-key signature by its grantor.
type Verifier interface {
	VerifyRoot(text string, sig []byte) error
	VerifyLink(grantorPrincipal string, text string, sig []byte) error
}

// Record is a stored grant: its content address, signed text and signature.
type Record struct {
	Digest string
	Text   string
	Sig    []byte
	Root   bool
}

// Resolver looks a record up by digest.
type Resolver func(digest string) (Record, bool)

// maxChainDepth bounds the walk; real chains are a handful of spawns deep.
const maxChainDepth = 32

// Walk verifies the chain from start up to a card-signed root and returns the
// start grant, which is the effective one. Every failure names the record
// digest and the check that broke.
func Walk(now time.Time, start Record, resolve Resolver, v Verifier) (Grant, error) {
	var effective Grant
	var child Grant
	visited := map[string]bool{}
	rec := start
	for depth := 1; ; depth++ {
		if depth > maxChainDepth {
			return Grant{}, fmt.Errorf("record %s: chain deeper than %d records", rec.Digest, maxChainDepth)
		}
		if visited[rec.Digest] {
			return Grant{}, fmt.Errorf("record %s: cycle in prev chain", rec.Digest)
		}
		visited[rec.Digest] = true

		g, err := verifyRecord(now, rec, v)
		if err != nil {
			return Grant{}, err
		}
		if depth == 1 {
			effective = g
		} else if err := Narrows(g, rec.Digest, child); err != nil {
			return Grant{}, fmt.Errorf("record %s: narrowing edge from child %s: %w", rec.Digest, child.Nonce, err)
		}
		if rec.Root {
			return effective, nil
		}

		parent, ok := resolve(g.Prev)
		if !ok {
			return Grant{}, fmt.Errorf("record %s: parent %s not found", rec.Digest, g.Prev)
		}
		if parent.Digest != g.Prev {
			return Grant{}, fmt.Errorf("record %s: resolver returned digest %s for parent %s", rec.Digest, parent.Digest, g.Prev)
		}
		child, rec = g, parent
	}
}

func verifyRecord(now time.Time, rec Record, v Verifier) (Grant, error) {
	g, err := ParseCanonicalText(rec.Text)
	if err != nil {
		return Grant{}, fmt.Errorf("record %s: parse: %w", rec.Digest, err)
	}
	if !now.Before(g.Expires) {
		return Grant{}, fmt.Errorf("record %s: expired at %s", rec.Digest, g.Expires.Format(timeLayout))
	}
	switch {
	case rec.Root && g.Prev != noValue:
		return Grant{}, fmt.Errorf("record %s: root record has prev %q, want %q", rec.Digest, g.Prev, noValue)
	case !rec.Root && g.Prev == noValue:
		return Grant{}, fmt.Errorf("record %s: non-root record has prev %q", rec.Digest, noValue)
	}
	if rec.Root {
		err = v.VerifyRoot(rec.Text, rec.Sig)
	} else {
		err = v.VerifyLink(g.Grantor, rec.Text, rec.Sig)
	}
	if err != nil {
		return Grant{}, fmt.Errorf("record %s: signature: %w", rec.Digest, err)
	}
	return g, nil
}
