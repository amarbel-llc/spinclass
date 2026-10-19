package profilegrant

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"code.linenisgreat.com/piggy/go/pkgs/markl"
)

// DigestOf is a record's content address: the markl-id of the SHA-256 of its
// canonical text, "piggy-record-digest-v1@sha256-<blech32>".
func DigestOf(text string) (string, error) {
	sum := sha256.Sum256([]byte(text))
	var id markl.Id
	if err := id.SetPurposeId(markl.PurposePiggyRecordDigestV1); err != nil {
		return "", err
	}
	if err := id.SetMarklId(markl.FormatIdHashSha256, sum[:]); err != nil {
		return "", fmt.Errorf("record digest: %w", err)
	}
	marshaled, err := id.MarshalText()
	if err != nil {
		return "", fmt.Errorf("record digest: %w", err)
	}
	return string(marshaled), nil
}

// ActText returns the exact text the operator's card signs for g, whose record
// digest is digest. It is a pure function of its arguments so the prompt the
// operator read cannot differ from the record it authorizes. The text names
// the request record by digest and states the right and its bound in words
// (class piggy-escalation-v1; piggy RFC 0012: UTF-8, at most 4096 bytes, only
// newline and tab as control characters).
func ActText(g Grant, digest string) (string, error) {
	if err := validateForAct(g, digest); err != nil {
		return "", err
	}
	session := "no session"
	if g.Session != noSession {
		session = "session " + g.Session
	}
	onward := "no"
	if g.Onward == OnwardGrant {
		onward = "yes"
	}
	form := "initial grant"
	if g.Widens != noValue {
		form = "widening of " + g.Widens
	}
	var b strings.Builder
	b.WriteString("spinclass profile grant (escalation): " + form + "\n")
	b.WriteString("record: " + digest + "\n")
	b.WriteString("allow: " + g.Grantee + " (" + session + ")\n")
	// Each profile is shown with the content digest it is pinned to: the
	// record id commits to them, but the operator reading the prompt must see
	// what is being pinned.
	b.WriteString("to spawn workers with these profiles, each pinned to its content digest:\n")
	for _, n := range g.profileNames() {
		b.WriteString("  " + n + " = " + g.Profiles[n] + "\n")
	}
	b.WriteString("may pass these on: " + onward + "\n")
	b.WriteString("until: " + g.Expires.UTC().Format(timeLayout) + "\n")
	b.WriteString("Signing this is an operator grant of these rights, bounded as stated above.\n")
	out := b.String()
	if len(out) > maxTextBytes {
		return "", fmt.Errorf("act text is %d bytes, limit %d", len(out), maxTextBytes)
	}
	return out, nil
}

func validateForAct(g Grant, digest string) error {
	if len(g.Profiles) == 0 {
		return errors.New("grant has no profiles")
	}
	if g.Onward != OnwardNone && g.Onward != OnwardGrant {
		return fmt.Errorf("onward %q, want none or grant", g.Onward)
	}
	if g.Expires.IsZero() {
		return errors.New("grant has no expiry")
	}
	checks := []struct{ what, value string }{
		{"record digest", digest}, {"grantee", g.Grantee}, {"session", g.Session}, {"widens", g.Widens},
	}
	for _, n := range g.profileNames() {
		checks = append(checks,
			struct{ what, value string }{"profile name", n},
			struct{ what, value string }{"profile digest of " + n, g.Profiles[n]},
		)
	}
	for _, c := range checks {
		if err := checkActField(c.what, c.value); err != nil {
			return err
		}
	}
	return nil
}

// checkActField rejects empty values and any control character, so no field
// can inject a line into the text the operator reads.
func checkActField(what, v string) error {
	if v == "" {
		return fmt.Errorf("%s is empty", what)
	}
	if !utf8.ValidString(v) {
		return fmt.Errorf("%s is not valid UTF-8", what)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains control character %U", what, r)
		}
	}
	return nil
}
