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
	b.WriteString("from: " + g.Grantor + "\n")
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
	if lines := renderedLines(out); lines > MaxActLines {
		return "", fmt.Errorf("act text renders as %d lines (each line wrapped at %d columns), budget %d (piggy's askpass shows at most 24 lines; piggy#322): "+
			"grant fewer profiles per record", lines, actWrapColumns, MaxActLines)
	}
	if chars := utf8.RuneCountInString(out); chars > MaxActChars {
		return "", fmt.Errorf("act text is %d characters, budget %d (piggy#322): grant fewer profiles per record", chars, MaxActChars)
	}
	return out, nil
}

// MaxActLines and MaxActChars are the act-text budget piggy gave for the
// operator's prompt: about 15 lines and 1200 characters, inside the 24-line
// askpass limit it enforces (piggy#322, relayed by the coordinator). They are
// treated as hard caps. ActText fails above either, so bless refuses before the
// card is asked anything.
const (
	MaxActLines = 15
	MaxActChars = 1200
)

// actFixedLines is the count of lines in an act text that are not profiles.
const actFixedLines = 8

// actWrapColumns is the width piggy wraps an act text line at when it counts
// rendered lines: a line takes ceil(length / 80) of them (an empty line, one).
const actWrapColumns = 80

// renderedLines counts the lines text takes on piggy's prompt, wrapped the way
// piggy counts them, so spinclass's budget and piggy's limit agree.
func renderedLines(text string) int {
	total := 0
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		n := utf8.RuneCountInString(line)
		total += max(1, (n+actWrapColumns-1)/actWrapColumns)
	}
	return total
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
		{"record digest", digest}, {"grantor", g.Grantor}, {"grantee", g.Grantee}, {"session", g.Session}, {"widens", g.Widens},
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

// checkActField applies the identity allowlist, so no field can inject a line
// or fake the punctuation of the text the operator reads.
func checkActField(what, v string) error {
	return checkIdentityValue(what, v)
}
