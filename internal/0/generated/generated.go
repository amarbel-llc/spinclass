// Package generated recognizes files a code generator owns, so a formatter
// pass can leave them as the generator wrote them.
package generated

import (
	"bytes"
	"regexp"
)

var goMarker = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// HasGoMarker reports whether src carries Go's generated-code marker
// (https://go.dev/s/generatedcode): a line matching
// `^// Code generated .* DO NOT EDIT\.$` before the first line that is neither
// blank nor a `//` line comment. A marker inside a `/* */` comment does not
// count.
func HasGoMarker(src []byte) bool {
	for len(src) > 0 {
		var line []byte
		line, src, _ = bytes.Cut(src, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if goMarker.Match(line) {
			return true
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 && !bytes.HasPrefix(trimmed, []byte("//")) {
			return false
		}
	}
	return false
}
