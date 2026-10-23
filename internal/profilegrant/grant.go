// Package profilegrant implements the pure chain rules for profile grants
// (FDR 0034, "Authority"): the canonical record text, the narrowing relation
// between a grant and its parent, and the walk from a presented grant to a
// card-signed root. It does no I/O and no cryptography beyond hashing for the
// record digest; signature checking is injected through Verifier.
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
	noSession    = "-"
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
	// Widens is the digest of the earlier root record this one widens, "-"
	// for none. It lets the signed act text tell an initial grant from a
	// widening, which the sshsig namespace does not.
	Widens string
	Prev   string // digest of the parent grant, "-" for a card root
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
	line("widens", g.Widens)
	line("prev", g.Prev)
	return b.String()
}

var lineKeys = []string{
	"grantor", "grantee", "session", "profiles", "rights",
	"onward", "expires", "nonce", "widens", "prev",
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
		switch key {
		case "profiles", "rights", "onward", "expires":
			// checked structurally below
		default:
			if err := checkIdentityValue(key, value); err != nil {
				return Grant{}, err
			}
		}
	}

	g := Grant{
		Grantor: values["grantor"],
		Grantee: values["grantee"],
		Session: values["session"],
		Nonce:   values["nonce"],
		Widens:  values["widens"],
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
	// Strict inverse: text that parses but would render differently (for
	// example fractional seconds) is not what a card signs or a verifier trusts.
	if g.CanonicalText() != s {
		return Grant{}, errors.New("text is not in canonical form")
	}
	return g, nil
}

// checkIdentityValue is the allowlist for every identity-ish value (grantor,
// grantee, session, nonce, widens, prev, profile names and digests). An
// allowlist, not a control-character blocklist, so spaces, parentheses, '=',
// commas and Unicode line breaks or bidi controls can never reshape the lines
// the operator reads or make two different grants render alike.
func checkIdentityValue(what, v string) error {
	if v == "" {
		return fmt.Errorf("%s is empty", what)
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("._:/@+-", r):
		default:
			return fmt.Errorf("%s contains disallowed character %U", what, r)
		}
	}
	return nil
}

func parseProfiles(value string) (map[string]string, error) {
	profiles := map[string]string{}
	previous := ""
	for _, field := range strings.Split(value, " ") {
		name, digest, ok := strings.Cut(field, "=")
		if !ok || name == "" || digest == "" {
			return nil, fmt.Errorf("profile entry %q, want name=digest", field)
		}
		if err := checkIdentityValue("profile name", name); err != nil {
			return nil, err
		}
		if err := checkIdentityValue("profile digest of "+name, digest); err != nil {
			return nil, err
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

// WalkOptions carries optional inputs to Narrows and Walk. The zero value is
// valid and means "no principal-to-session mapping".
type WalkOptions struct {
	// SessionOfPrincipal maps a principal to the session key it runs in. A
	// spawn-time grant names its grantee by session key (the child's principal
	// is minted only when its clown boots), while the next link's grantor is a
	// principal; this mapping joins the two.
	SessionOfPrincipal func(principal string) (sessionKey string, ok bool)

	// BaseChain returns the _base chain of a profile definition, profile@digest
	// excluded, nearest base first, as clown resolved it with the profile (never
	// re-resolved by name: a user profile may shadow a builtin's). Each link
	// carries how the step below it widens that base; an error means the
	// definition cannot be checked (its live digest is no longer digest, or it
	// does not resolve). nil keeps the pre-stacking rule: a child profile must
	// be held by the parent (D1 stacking, FDR 0034 "Verification").
	BaseChain func(profile, digest string) ([]BaseLink, error)
}

// BaseLink is one step up a profile's _base chain: the base's name and
// digest, and every way the profile one step down widens it (empty for a
// narrowing).
type BaseLink struct {
	Name, Digest string
	Widenings    []string
}

// maxBaseDepth bounds a _base chain, as maxChainDepth bounds a grant chain.
const maxBaseDepth = 8

// bindsGrantor reports whether child's grantor is the parent's grantee,
// directly or through the session mapping. The error text says when a mapping
// was consulted and what it answered.
func (o WalkOptions) bindsGrantor(parent, child Grant) error {
	if child.Grantor == parent.Grantee {
		return nil
	}
	plain := fmt.Sprintf("child grantor %q is not parent grantee %q", child.Grantor, parent.Grantee)
	if o.SessionOfPrincipal == nil {
		return errors.New(plain)
	}
	session, ok := o.SessionOfPrincipal(child.Grantor)
	switch {
	case !ok:
		return fmt.Errorf("%s (session mapping has no session for grantor %q)", plain, child.Grantor)
	case session != parent.Grantee:
		return fmt.Errorf("%s (session mapping puts grantor %q in session %q)", plain, child.Grantor, session)
	}
	return nil
}

// Narrows returns nil iff child is a valid narrowing of parent, whose digest
// is parentDigest. Narrowing is the only change a software key may sign;
// anything wider needs the card.
//
// A holder may always USE its own profiles to spawn workers; that is checked
// with Holds, not by creating a link. A link exists only to give the child its
// own right to pass on or spawn, which requires the parent's onward: grant. So
// a parent with onward none can have no children at all.
func Narrows(parent Grant, parentDigest string, child Grant, opts WalkOptions) error {
	if child.Prev != parentDigest {
		return fmt.Errorf("child prev %q is not parent digest %q", child.Prev, parentDigest)
	}
	// Bind the chain by principal: only the parent's grantee may narrow it
	// onward. Without this, anyone who learned a parent digest could sign a
	// valid-looking child under it and borrow the grantee's rights.
	if err := opts.bindsGrantor(parent, child); err != nil {
		return err
	}
	if parent.Onward != OnwardGrant {
		return errors.New("parent may not pass these on (onward none)")
	}
	if child.Expires.After(parent.Expires) {
		return fmt.Errorf("child expiry after parent expiry (child %s, parent %s)",
			child.Expires.UTC().Format(timeLayout), parent.Expires.UTC().Format(timeLayout))
	}
	for _, name := range child.profileNames() {
		childDigest := child.Profiles[name]
		parentDigestOfProfile, held := parent.Profiles[name]
		if !held {
			if err := opts.reachesHeldBase(parent, name, childDigest); err != nil {
				return err
			}
			continue
		}
		if childDigest != parentDigestOfProfile {
			return fmt.Errorf("profile %s digest differs (parent %s, child %s)",
				name, parentDigestOfProfile, childDigest)
		}
	}
	return nil
}

// reachesHeldBase is the D1 stacking rule for a child profile the parent does
// not hold: its _base chain must reach a profile the parent holds, at the
// digest the parent holds it, and no edge on the way may widen its base (the
// per-edge catalog check, V-D1-6/V-D1-10). Rights stay derived from profile
// names and are judged along that chain (D1 interim, FDR 0034
// "Verification"): the child gains nothing the held base does not grant.
func (o WalkOptions) reachesHeldBase(parent Grant, name, digest string) error {
	if o.BaseChain == nil {
		return fmt.Errorf("profile %s not held by parent", name)
	}
	chain, err := o.BaseChain(name, digest)
	if err != nil {
		return fmt.Errorf("profile %s not held by parent; its base chain cannot be checked: %w", name, err)
	}
	if len(chain) > maxBaseDepth {
		return fmt.Errorf("profile %s: base chain longer than %d", name, maxBaseDepth)
	}
	names := make([]string, 0, len(chain))
	for _, link := range chain {
		if len(link.Widenings) > 0 {
			return fmt.Errorf("profile %s widens base %s by: %s", name, link.Name, strings.Join(link.Widenings, "; "))
		}
		if heldDigest, held := parent.Profiles[link.Name]; held {
			if heldDigest != link.Digest {
				return fmt.Errorf("profile %s: the parent holds base %s at digest %s, but %s's base chain reaches it at %s",
					name, link.Name, heldDigest, name, link.Digest)
			}
			return nil
		}
		names = append(names, link.Name)
	}
	return fmt.Errorf("profile %s not held by parent, and its base chain (%s) reaches no profile the parent holds",
		name, strings.Join(names, ", "))
}

// RootVerifier checks the operator card's signature. actText is the text
// REGENERATED from the record by ActText, never text supplied by the signer, so
// a signature over any other text cannot authorize the record.
type RootVerifier interface {
	VerifyRoot(actText string, sig []byte) error
}

// LinkVerifier checks the signature on a non-root link.
type LinkVerifier interface {
	VerifyLink(g Grant, sig []byte) error
}

// Verifier checks both kinds of record signature.
type Verifier interface {
	RootVerifier
	LinkVerifier
}

// LocalLinkVerifier accepts only unsigned links. Narrowing links carry no
// signature until principals have keys (FDR 0034, Phase 2 design item 4):
// authority comes from the card-signed root and the subset relation alone, so
// a link is only as unforgeable as write access to the local grant store, the
// same bar as FDR 0032 slice 0. Replace it when per-principal keys exist.
type LocalLinkVerifier struct{}

// VerifyLink returns nil iff sig is empty.
func (LocalLinkVerifier) VerifyLink(_ Grant, sig []byte) error {
	if len(sig) != 0 {
		return errors.New("link carries a signature but no link verifier is configured")
	}
	return nil
}

type composedVerifier struct {
	root func(actText string, sig []byte) error
	link LinkVerifier
}

func (c composedVerifier) VerifyRoot(actText string, sig []byte) error { return c.root(actText, sig) }

func (c composedVerifier) VerifyLink(g Grant, sig []byte) error { return c.link.VerifyLink(g, sig) }

// NewVerifier composes a root-checking function and a link verifier into a
// Verifier.
func NewVerifier(root func(actText string, sig []byte) error, link LinkVerifier) Verifier {
	return composedVerifier{root: root, link: link}
}

// Record is a stored grant: its content address, canonical text and signature.
// Digest must equal DigestOf(Text). For a root record Sig is the operator
// card's armored sshsig over ActText(grant, Digest); for a link it is empty
// until principals have keys.
type Record struct {
	Digest string `json:"digest"`
	Text   string `json:"text"`
	Sig    []byte `json:"sig,omitempty"` // base64 in JSON
	Root   bool   `json:"root"`
}

// Resolver looks a record up by digest.
type Resolver func(digest string) (Record, bool)

// maxChainDepth bounds the walk; real chains are a handful of spawns deep.
const maxChainDepth = 32

// Walk verifies the chain from start up to a card-signed root and returns the
// start grant, which is the effective one. Every failure names the record
// digest and the check that broke.
func Walk(now time.Time, start Record, resolve Resolver, v Verifier, opts WalkOptions) (Grant, error) {
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
		if err := checkWidens(now, rec, g, resolve); err != nil {
			return Grant{}, err
		}
		if depth == 1 {
			effective = g
		} else if err := Narrows(g, rec.Digest, child, opts); err != nil {
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

// checkWidens enforces that only a root record may widen, and that it widens
// a live root record for the same grantee whose profile names it still
// includes (digests may differ: a widening may re-pin). The widened record is
// judged on its own card signature, so its chain is not walked here.
func checkWidens(now time.Time, rec Record, g Grant, resolve Resolver) error {
	if g.Widens == noValue {
		return nil
	}
	if !rec.Root {
		return fmt.Errorf("record %s: non-root record has widens %q, want %q", rec.Digest, g.Widens, noValue)
	}
	widened, ok := resolve(g.Widens)
	if !ok {
		return fmt.Errorf("record %s: widened record %s not found", rec.Digest, g.Widens)
	}
	if widened.Digest != g.Widens {
		return fmt.Errorf("record %s: resolver returned digest %s for widened record %s", rec.Digest, widened.Digest, g.Widens)
	}
	want, err := DigestOf(widened.Text)
	if err != nil {
		return fmt.Errorf("record %s: widened record %s: digest: %w", rec.Digest, g.Widens, err)
	}
	if want != widened.Digest {
		return fmt.Errorf("record %s: widened record %s: digest mismatch: text hashes to %s", rec.Digest, g.Widens, want)
	}
	if !widened.Root {
		return fmt.Errorf("record %s: widened record %s is not a root record", rec.Digest, g.Widens)
	}
	wg, err := ParseCanonicalText(widened.Text)
	if err != nil {
		return fmt.Errorf("record %s: widened record %s: parse: %w", rec.Digest, g.Widens, err)
	}
	if !now.Before(wg.Expires) {
		return fmt.Errorf("record %s: widened record %s expired at %s", rec.Digest, g.Widens, wg.Expires.Format(timeLayout))
	}
	for _, name := range wg.profileNames() {
		if _, ok := g.Profiles[name]; !ok {
			return fmt.Errorf("record %s: widens %s but drops profile %s", rec.Digest, g.Widens, name)
		}
	}
	if wg.Grantee != g.Grantee {
		return fmt.Errorf("record %s: widens %s but grantee %q differs from widened grantee %q", rec.Digest, g.Widens, g.Grantee, wg.Grantee)
	}
	return nil
}

func verifyRecord(now time.Time, rec Record, v Verifier) (Grant, error) {
	// A resolver must not be able to return forged text under a claimed digest.
	want, err := DigestOf(rec.Text)
	if err != nil {
		return Grant{}, fmt.Errorf("record %s: digest: %w", rec.Digest, err)
	}
	if rec.Digest != want {
		return Grant{}, fmt.Errorf("record %s: digest mismatch: text hashes to %s", rec.Digest, want)
	}
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
		var act string
		if act, err = ActText(g, rec.Digest); err == nil {
			err = v.VerifyRoot(act, rec.Sig)
		}
	} else {
		err = v.VerifyLink(g, rec.Sig)
	}
	if err != nil {
		return Grant{}, fmt.Errorf("record %s: signature: %w", rec.Digest, err)
	}
	return g, nil
}
