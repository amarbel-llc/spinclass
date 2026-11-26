package keyholder

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A record a session can write reaches the operator's terminal and the lifecycle
// log: record.json sits in a directory every unconfined session of the account
// can write, and the key history and the certificate's text are read from files
// of the same kind. Whatever such a file says is data, never a line of output:
// these two functions are the only way a record field is printed.

// maxSafeSession is the longest session key printed as it is: a real key is
// <repo>/<branch>, far below it.
const maxSafeSession = 200

// altered is appended to a field that did not look like what spinclass wrote.
const altered = " (record altered)"

// SafeSession returns s unchanged when it is made only of the session-key
// alphabet (letters, digits and / - _ . + @ :) and is under 200 bytes. Anything
// else is a record somebody altered: it is returned quoted and escaped, cut to a
// short prefix, and followed by " (record altered)", so an escape sequence, a
// newline, a NUL or a bidirectional override never reaches a terminal or a log
// as itself.
func SafeSession(s string) string {
	if len(s) < maxSafeSession && inSessionAlphabet(s) {
		return s
	}
	return strconv.Quote(shortPrefix(s, 80)) + altered
}

func inSessionAlphabet(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '/', c == '-', c == '_', c == '.', c == '+', c == '@', c == ':':
		default:
			return false
		}
	}
	return true
}

// shortPrefix is at most n bytes of s, cut on a rune boundary.
func shortPrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// maxSafeLine bounds a free-text field printed beside a record's own words.
const maxSafeLine = 400

// SafeLine is the same guard for free text a record or a program put in a
// message (an ssh-keygen complaint, a fingerprint read from a history file):
// control and format characters (escape, newline, NUL, bidirectional overrides,
// invalid UTF-8) are shown as escapes, never as themselves, and the text is cut
// at 400 bytes. Text that needs no change is returned as it is.
func SafeLine(s string) string {
	clean := len(s) <= maxSafeLine && utf8.ValidString(s)
	if clean {
		for _, r := range s {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				clean = false
				break
			}
		}
	}
	if clean {
		return s
	}
	s = shortPrefix(s, maxSafeLine)
	var b strings.Builder
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteString(`\x` + strconv.FormatUint(uint64(s[0]), 16))
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			q := strconv.QuoteRune(r) // 'x', escaped when not printable
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteRune(r)
		}
		s = s[size:]
	}
	return b.String()
}
