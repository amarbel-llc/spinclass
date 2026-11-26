package keyholder

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"
)

// rlo is U+202E (right-to-left override) spelled as its UTF-8 bytes, so no
// invisible character appears literally in this source.
const rlo = "\xe2\x80\xae"

// noRawControl reports a control or format character left in s as itself (a
// line break included: a printed record field must never start a line).
func noRawControl(s string) (rune, bool) {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return r, false
		}
	}
	return 0, true
}

func TestSafeSessionLeavesARealSessionKeyAlone(t *testing.T) {
	for _, s := range []string{"", "repo/branch", "worker/kid", "r.e-p_o/feat+x@host:1", strings.Repeat("a", 199)} {
		if got := SafeSession(s); got != s {
			t.Errorf("SafeSession(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestSafeSessionQuotesAnythingElseAndSaysTheRecordWasAltered(t *testing.T) {
	cases := map[string]string{
		"escape sequence": "repo/x\x1b[31mRED",
		"newline":         "repo/x\nFORGED LINE: stopped everything",
		"carriage return": "repo/x\rFORGED",
		"nul":             "repo/x\x00y",
		"override":        "repo/x" + rlo + "gnp.exe",
		"space":           "repo/x y",
		"invalid utf-8":   "repo/\xff\xfe",
		"over long":       strings.Repeat("a", 200),
		"way over long":   strings.Repeat("a", 5000),
	}
	for name, in := range cases {
		got := SafeSession(in)
		if r, ok := noRawControl(got); !ok {
			t.Errorf("%s: output %q holds a raw control or format character %U", name, got, r)
		}
		if !strings.HasSuffix(got, " (record altered)") {
			t.Errorf("%s: output %q does not say the record was altered", name, got)
		}
		if len(got) > 200 {
			t.Errorf("%s: output is %d bytes, want a short prefix", name, len(got))
		}
	}
	if got := SafeSession("repo/x\x1b[31mRED"); !strings.Contains(got, `\x1b`) {
		t.Errorf("the escape is not shown as an escape: %q", got)
	}
}

func TestSafeLineEscapesControlAndFormatCharactersAndKeepsPlainText(t *testing.T) {
	if got := SafeLine("ssh-keygen: bad option -x"); got != "ssh-keygen: bad option -x" {
		t.Errorf("plain text changed: %q", got)
	}
	if got := SafeLine("café ✓"); got != "café ✓" {
		t.Errorf("printable non-ASCII changed: %q", got)
	}
	for _, in := range []string{"a\x1b[2Jb", "a\nb", "a\x00b", "a" + rlo + "b", "a\xffb", "a\tb"} {
		got := SafeLine(in)
		if r, ok := noRawControl(got); !ok {
			t.Errorf("SafeLine(%q) = %q holds a raw %U", in, got, r)
		}
		if strings.ContainsRune(got, 0xfffd) {
			t.Errorf("SafeLine(%q) = %q holds a replacement character", in, got)
		}
	}
	if got := SafeLine(strings.Repeat("x", 5000)); len(got) > 400 {
		t.Errorf("a long line is %d bytes, want at most 400", len(got))
	}
}

// The lifecycle log is one line per event: a record's session key must not be
// able to end a line or write another.
func TestTheLifecycleLogNeverCarriesAControlCharacterFromAnArgument(t *testing.T) {
	var lines []string
	l := &Launcher{Log: func(format string, a ...any) { lines = append(lines, fmt.Sprintf(format, a...)) }}
	l.logf("keyholder: stopped the pair %s (%s)", "abc", "repo/x\x1b[31m\nkeyholder: FORGED line "+rlo)
	l.logf("keyholder: failed: %s", fmt.Errorf("systemctl said:\nFORGED\x1b[0m"))
	if len(lines) != 2 {
		t.Fatalf("%d lines logged, want 2", len(lines))
	}
	for _, ln := range lines {
		if r, ok := noRawControl(ln); !ok {
			t.Errorf("log line %q holds a raw %U", ln, r)
		}
	}
}

func TestANoteNeverCarriesAControlCharacterFromTheHistoryOrTheCertificate(t *testing.T) {
	evil := "SHA256:abc\x1b[31m\nFORGED" + rlo
	for name, h := range map[string]Health{
		"stopped":  {Stopped: true, StoppedAt: time.Unix(0, 0), Fingerprint: evil},
		"reminted": {Reminted: true, RemintedAt: time.Unix(0, 0), OldFingerprint: evil, Fingerprint: evil},
		"other":    {Fingerprint: evil, Cert: CertInfo{State: CertOtherKey, Fingerprint: evil}},
		"problem":  {Cert: CertInfo{State: CertUnreadable, Problem: evil}},
	} {
		n := h.Note()
		if n == "" {
			t.Errorf("%s: no note", name)
			continue
		}
		if r, ok := noRawControl(n); !ok {
			t.Errorf("%s: note %q holds a raw %U", name, n, r)
		}
	}
}
