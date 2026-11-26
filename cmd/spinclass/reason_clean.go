package main

import (
	"strings"
	"unicode/utf8"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// A reason a confined session gives for a restart or a stop is text its agent
// wrote (A-M2). It reaches the frontend record, the argv of the trusted
// hand-off and the wakes spinclass sends to holders in its own voice, so for a
// confined caller it is bounded, cleaned and marked before it is stored.
// An unconfined caller's reason is left exactly as given.

// maxConfinedReasonBytes bounds the agent's own words: 200 bytes, cut on a rune
// boundary, with an ellipsis when cut. The marker below is added after the cut.
const maxConfinedReasonBytes = 200

// confinedReasonMark says, wherever the reason is shown, whose words they are.
const confinedReasonMark = "from the confined session: "

// backslash is spelled as a constant so the two escape texts below are built
// from plain ASCII: no invisible character, and no \u sequence a tool could
// turn into one, appears in this file.
const backslash = "\\"

// lineSeparators are the two line-separator runes (U+2028, U+2029) as bytes.
// They are neither control nor format characters to the unicode tables, so
// SafeLine lets them through; this shows them as escapes too.
var lineSeparators = strings.NewReplacer(
	"\xe2\x80\xa8", backslash+"u2028",
	"\xe2\x80\xa9", backslash+"u2029",
)

// cleanConfinedReason returns the marked, bounded, escaped form of reason:
// control characters, escape sequences, newlines, NUL, bidirectional overrides,
// line separators and invalid UTF-8 are shown as escapes, never as themselves.
func cleanConfinedReason(reason string) string {
	bounded := reason
	if len(bounded) > maxConfinedReasonBytes {
		cut := maxConfinedReasonBytes
		for cut > 0 && !utf8.RuneStart(bounded[cut]) {
			cut--
		}
		bounded = bounded[:cut] + "..."
	}
	return confinedReasonMark + lineSeparators.Replace(keyholder.SafeLine(bounded))
}
