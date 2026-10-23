package profilegrant

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	transitionNow = time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)
	realProfile1  = "clown-profile-digest-v1@sha256-" + strings.Repeat("a", 52)
	realProfile2  = "clown-profile-digest-v1@sha256-" + strings.Repeat("c", 52)
)

func testTransition() Transition {
	return Transition{
		Profile: "fable-design",
		From:    realProfile1,
		To:      realProfile2,
		Expires: transitionNow.Add(24 * time.Hour),
		Nonce:   "00112233445566778899aabbccddeeff",
	}
}

// A transition is an approval, not a grant: no grantor, grantee, rights or
// prev, so it can never be a link in a grant chain (#407, D1 review).
func TestTransitionCanonicalTextRoundTrips(t *testing.T) {
	tr := testTransition()
	text := tr.CanonicalText()
	const want = "spinclass profile-transition v1\n" +
		"profile: fable-design\n" +
		"from: clown-profile-digest-v1@sha256-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"to: clown-profile-digest-v1@sha256-cccccccccccccccccccccccccccccccccccccccccccccccccccc\n" +
		"expires: 2026-10-24T16:00:00Z\n" +
		"nonce: 00112233445566778899aabbccddeeff\n"
	if text != want {
		t.Fatalf("canonical text:\n%s\nwant:\n%s", text, want)
	}
	got, err := ParseTransitionText(text)
	if err != nil {
		t.Fatal(err)
	}
	if got != tr {
		t.Errorf("round trip = %+v, want %+v", got, tr)
	}
	if _, err := ParseCanonicalText(text); err == nil {
		t.Error("a transition parsed as a grant: it must never enter a grant chain")
	}
}

func TestParseTransitionTextIsStrict(t *testing.T) {
	good := testTransition().CanonicalText()
	same := testTransition()
	same.To = same.From
	for name, text := range map[string]string{
		"grant header":     strings.Replace(good, "spinclass profile-transition v1", header, 1),
		"no final newline": strings.TrimSuffix(good, "\n"),
		"space in profile": strings.Replace(good, "profile: fable-design", "profile: fable design", 1),
		"bidi in to":       strings.Replace(good, "to: clown", "to: ‮clown", 1),
		"same from and to": same.CanonicalText(),
		"fractional time":  strings.Replace(good, "16:00:00Z", "16:00:00.5Z", 1),
		"extra line":       good + "grantee: x\n",
		"reordered":        strings.Replace(good, "profile: fable-design\nfrom:", "from:", 1),
	} {
		if _, err := ParseTransitionText(text); err == nil {
			t.Errorf("%s: parsed, want a refusal", name)
		}
	}
}

func TestTransitionActTextGolden(t *testing.T) {
	tr := testTransition()
	tr.From, tr.To = "d-old", "d-new"
	got, err := TransitionActText(tr, goldenDigest, []string{"model: changed claude-opus -> claude-fable", `env "FOO": added`})
	if err != nil {
		t.Fatal(err)
	}
	const want = `spinclass profile transition (escalation): fable-design
record: piggy-record-digest-v1@sha256-example
from: d-old
to: d-new
redeemable until: 2026-10-24T16:00:00Z
model: changed claude-opus -> claude-fable
env "FOO": added
Signing this approves the change for every session pinned to "from".
`
	if got != want {
		t.Fatalf("act text:\n%s\nwant:\n%s", got, want)
	}
}

// Real digests wrap to two rendered lines each, so the fixed part costs 9 of
// the 15 lines and leaves exactly MaxTransitionDiffLines for clown's diff.
func TestTransitionActTextFitsTheBudgetWithAFullDiff(t *testing.T) {
	realRecord := "piggy-record-digest-v1@sha256-" + strings.Repeat("b", 52)
	diff := make([]string, MaxTransitionDiffLines)
	for i := range diff {
		diff[i] = strings.Repeat("x", 80)
	}
	got, err := TransitionActText(testTransition(), realRecord, diff)
	if err != nil {
		t.Fatalf("a full %d-line diff with real digests must fit: %v", MaxTransitionDiffLines, err)
	}
	if n := renderedLines(got); n > MaxActLines {
		t.Errorf("rendered %d lines, budget %d", n, MaxActLines)
	}
	if _, err := TransitionActText(testTransition(), realRecord, append(diff, "one more")); err == nil {
		t.Error("a diff over MaxTransitionDiffLines must be refused")
	}
}

func TestTransitionActTextRefusesUnsafeDiffLines(t *testing.T) {
	for name, line := range map[string]string{
		"newline":   "a\nb",
		"tab":       "a\tb",
		"control":   "a\x1bb",
		"bidi":      "a‮b",
		"too long":  strings.Repeat("x", 81),
		"empty":     "",
		"non-ascii": "café",
	} {
		if _, err := TransitionActText(testTransition(), goldenDigest, []string{line}); err == nil {
			t.Errorf("%s: diff line %q accepted", name, line)
		}
	}
	// The one non-ASCII rune clown's Diff emits is its elision mark.
	if _, err := TransitionActText(testTransition(), goldenDigest, []string{"url: changed https://a.example/lo…ng -> x"}); err != nil {
		t.Errorf("the elision mark must be accepted: %v", err)
	}
}

func signedTransition(t *testing.T, tr Transition, diff []string) (TransitionRecord, func(string, []byte) error) {
	t.Helper()
	text := tr.CanonicalText()
	digest, err := DigestOf(text)
	if err != nil {
		t.Fatal(err)
	}
	act, err := TransitionActText(tr, digest, diff)
	if err != nil {
		t.Fatal(err)
	}
	// The fake card signs by echoing the act text; the verifier accepts only
	// a signature over exactly the text it regenerates.
	rec := TransitionRecord{Digest: digest, Text: text, Diff: diff, Sig: []byte(act)}
	verify := func(actText string, sig []byte) error {
		if string(sig) != actText {
			return errors.New("signature does not cover this act text")
		}
		return nil
	}
	return rec, verify
}

func TestVerifyTransition(t *testing.T) {
	diff := []string{"model: changed a -> b"}
	rec, verify := signedTransition(t, testTransition(), diff)

	got, err := VerifyTransition(transitionNow, rec, verify)
	if err != nil {
		t.Fatalf("genuine record refused: %v", err)
	}
	if !got.Covers("fable-design", realProfile1, realProfile2) {
		t.Errorf("verified transition %+v does not cover itself", got)
	}
	for _, c := range []struct{ profile, from, to string }{
		{"other", realProfile1, realProfile2},
		{"fable-design", realProfile2, realProfile1},
		{"fable-design", realProfile1, "clown-profile-digest-v1@sha256-dd"},
	} {
		if got.Covers(c.profile, c.from, c.to) {
			t.Errorf("transition covers %+v; it must cover only its exact transition", c)
		}
	}

	tampered := map[string]TransitionRecord{}
	m := rec
	m.Diff = []string{"model: changed a -> c"}
	tampered["altered diff"] = m
	m = rec
	m.Text = strings.Replace(rec.Text, "fable-design", "fable-review", 1)
	tampered["text without its digest"] = m
	m = rec
	m.Sig = nil
	tampered["unsigned"] = m
	for name, bad := range tampered {
		if _, err := VerifyTransition(transitionNow, bad, verify); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
	// The expiry bounds redemption only; a past redemption is the session's
	// own record and is not re-verified against it.
	if _, err := VerifyTransition(transitionNow.Add(25*time.Hour), rec, verify); err == nil || !strings.Contains(err.Error(), "redemption window") {
		t.Errorf("expired record: err = %v, want a redemption-window refusal", err)
	}
}

// A base profile change moves every dependent's digest, so each profile on
// the base chain needs its own transition: one for the base never covers a
// dependent, even across the same pair of digests (D1 review, point 5).
func TestTransitionForABaseDoesNotCoverItsDependents(t *testing.T) {
	base := testTransition()
	base.Profile = "builder"
	rec, verify := signedTransition(t, base, nil)
	got, err := VerifyTransition(transitionNow, rec, verify)
	if err != nil {
		t.Fatal(err)
	}
	if got.Covers("builder-reviewer", base.From, base.To) {
		t.Error("the base's transition covered a dependent profile")
	}
	if !got.Covers("builder", base.From, base.To) {
		t.Error("the base's transition did not cover the base")
	}
}
