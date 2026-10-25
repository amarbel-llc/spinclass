package profilegrant

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// A Transition is the operator's card-signed approval of one profile change
// (spinclass#407, clown FDR 0022 decision 5): profile, recorded digest From,
// current digest To. It is NOT a grant and carries no authority: no grantor,
// grantee, rights or prev, so it can never be a link in a grant chain, and it
// can never add rights (a change that needs more is a widening, a re-issued
// root). It is not bound to a session: any session pinned to From may be
// rebound to To by redeeming it before Expires. Expires bounds redemption
// only; a session keeps its own record of the rebind (session state), so a
// long-lived session outlives the approval. The nonce identifies the record;
// it is not single-use.
type Transition struct {
	Profile string
	From    string
	To      string
	Expires time.Time // UTC
	Nonce   string
}

const transitionHeader = "spinclass profile-transition v1"

var transitionKeys = []string{"profile", "from", "to", "expires", "nonce"}

// MaxTransitionDiffLines is how many of clown's field-diff lines the act text
// carries: real digests wrap to two rendered lines each, so the fixed part
// takes 9 of the MaxActLines budget.
const MaxTransitionDiffLines = 6

// elisionMark is the one non-ASCII rune clown's profilelookup.Diff emits.
const elisionMark = '…'

// CanonicalText renders the record body in the fixed line order its digest
// covers.
func (tr Transition) CanonicalText() string {
	var b strings.Builder
	line := func(key, value string) { b.WriteString(key + ": " + value + "\n") }
	b.WriteString(transitionHeader + "\n")
	line("profile", tr.Profile)
	line("from", tr.From)
	line("to", tr.To)
	line("expires", tr.Expires.UTC().Format(timeLayout))
	line("nonce", tr.Nonce)
	return b.String()
}

// ParseTransitionText is the strict inverse of CanonicalText.
func ParseTransitionText(s string) (Transition, error) {
	if len(s) > maxTextBytes {
		return Transition{}, fmt.Errorf("text is %d bytes, limit %d", len(s), maxTextBytes)
	}
	if !utf8.ValidString(s) || !strings.HasSuffix(s, "\n") {
		return Transition{}, errors.New("text is not valid UTF-8 ending in a newline")
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if lines[0] != transitionHeader {
		return Transition{}, fmt.Errorf("wrong header %q, want %q", lines[0], transitionHeader)
	}
	if len(lines)-1 != len(transitionKeys) {
		return Transition{}, fmt.Errorf("got %d body lines, want %d", len(lines)-1, len(transitionKeys))
	}
	values := make(map[string]string, len(transitionKeys))
	for i, key := range transitionKeys {
		value, ok := strings.CutPrefix(lines[i+1], key+": ")
		if !ok {
			return Transition{}, fmt.Errorf("line %d: want %q line, got %q", i+2, key, lines[i+1])
		}
		if key != "expires" {
			if err := checkIdentityValue(key, value); err != nil {
				return Transition{}, err
			}
		}
		values[key] = value
	}
	expires, err := time.Parse(timeLayout, values["expires"])
	if err != nil {
		return Transition{}, fmt.Errorf("expires: %w", err)
	}
	tr := Transition{
		Profile: values["profile"], From: values["from"], To: values["to"],
		Expires: expires.UTC(), Nonce: values["nonce"],
	}
	if tr.From == tr.To {
		return Transition{}, errors.New("from and to are the same digest: nothing to approve")
	}
	if tr.CanonicalText() != s {
		return Transition{}, errors.New("text is not in canonical form")
	}
	return tr, nil
}

// Covers reports whether tr approves exactly this change.
func (tr Transition) Covers(profile, from, to string) bool {
	return tr.Profile == profile && tr.From == from && tr.To == to
}

// TransitionActText is the exact text the operator's card signs for tr, whose
// record digest is digest, with clown's field diff (profilelookup.Diff) as
// diff. It is a pure function, so VerifyTransition regenerates it rather than
// trust any stored text. Each diff line must be printable ASCII (or clown's
// elision mark), at most 80 runes, so no line can reshape what the operator
// reads.
func TransitionActText(tr Transition, digest string, diff []string) (string, error) {
	if _, err := ParseTransitionText(tr.CanonicalText()); err != nil {
		return "", fmt.Errorf("transition record: %w", err)
	}
	if err := checkActField("record digest", digest); err != nil {
		return "", err
	}
	if len(diff) > MaxTransitionDiffLines {
		return "", fmt.Errorf("diff has %d lines, at most %d fit the act text", len(diff), MaxTransitionDiffLines)
	}
	for _, line := range diff {
		if err := checkDiffLine(line); err != nil {
			return "", err
		}
	}
	var b strings.Builder
	b.WriteString("spinclass profile transition (escalation): " + tr.Profile + "\n")
	b.WriteString("record: " + digest + "\n")
	b.WriteString("from: " + tr.From + "\n")
	b.WriteString("to: " + tr.To + "\n")
	b.WriteString("redeemable until: " + tr.Expires.UTC().Format(timeLayout) + "\n")
	for _, line := range diff {
		b.WriteString(line + "\n")
	}
	b.WriteString(`Signing this approves the change for every session pinned to "from".` + "\n")
	out := b.String()
	if len(out) > maxTextBytes {
		return "", fmt.Errorf("act text is %d bytes, limit %d", len(out), maxTextBytes)
	}
	if lines := renderedLines(out); lines > MaxActLines {
		return "", fmt.Errorf("act text renders as %d lines (each line wrapped at %d columns), budget %d (piggy#322)", lines, actWrapColumns, MaxActLines)
	}
	if chars := utf8.RuneCountInString(out); chars > MaxActChars {
		return "", fmt.Errorf("act text is %d characters, budget %d (piggy#322)", chars, MaxActChars)
	}
	return out, nil
}

func checkDiffLine(line string) error {
	if line == "" {
		return errors.New("diff line is empty")
	}
	if n := utf8.RuneCountInString(line); n > actWrapColumns {
		return fmt.Errorf("diff line is %d runes, at most %d", n, actWrapColumns)
	}
	for _, r := range line {
		if (r < 0x20 || r > 0x7e) && r != elisionMark {
			return fmt.Errorf("diff line contains disallowed character %U", r)
		}
	}
	return nil
}

// TransitionRecord is a stored approval: the record body, the field diff the
// operator was shown, and the card's signature. Digest covers Text only; the
// signature covers the act text, which includes Diff, so neither can change
// without failing VerifyTransition.
type TransitionRecord struct {
	Digest string   `json:"digest"`
	Text   string   `json:"text"`
	Diff   []string `json:"diff,omitempty"`
	Sig    []byte   `json:"sig"` // base64 in JSON
}

// CheckTransitionRecord is the internal consistency check a store applies on
// every read and write: the digest names the text and the text parses. It does
// not check the signature or the redemption window; VerifyTransition does.
func CheckTransitionRecord(rec TransitionRecord) (Transition, error) {
	want, err := DigestOf(rec.Text)
	if err != nil {
		return Transition{}, err
	}
	if rec.Digest != want {
		return Transition{}, fmt.Errorf("digest %q does not match text (hashes to %q)", rec.Digest, want)
	}
	return ParseTransitionText(rec.Text)
}

// VerifyTransition checks, for a redemption at now, that rec is consistent,
// inside its redemption window, and signed by the card over the act text
// regenerated from it; root is the card verifier (grantverify.NewRootVerifier).
func VerifyTransition(now time.Time, rec TransitionRecord, root func(actText string, sig []byte) error) (Transition, error) {
	tr, err := CheckTransitionRecord(rec)
	if err != nil {
		return Transition{}, fmt.Errorf("transition record %s: %w", rec.Digest, err)
	}
	if !now.Before(tr.Expires) {
		return Transition{}, fmt.Errorf("transition record %s: redemption window closed at %s", rec.Digest, tr.Expires.Format(timeLayout))
	}
	if len(rec.Sig) == 0 {
		return Transition{}, fmt.Errorf("transition record %s: not signed", rec.Digest)
	}
	act, err := TransitionActText(tr, rec.Digest, rec.Diff)
	if err != nil {
		return Transition{}, fmt.Errorf("transition record %s: %w", rec.Digest, err)
	}
	if err := root(act, rec.Sig); err != nil {
		return Transition{}, fmt.Errorf("transition record %s: card signature: %w", rec.Digest, err)
	}
	return tr, nil
}
