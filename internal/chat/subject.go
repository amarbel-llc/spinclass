package chat

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// SubjectMaxLen caps a message subject, in runes. The subject is the only
// part of a message carried in push-notification lines (the clown
// job-wakeup line), which the harness truncates somewhere past ~500
// characters (issue #103; the exact limit varies by event size) — 200 keeps
// a comfortable margin under every observed truncation after the line's
// prefix overhead, and reads as a one-line summary. The full body is always
// recoverable via chat-read.
const SubjectMaxLen = 200

// ValidateSubject rejects an explicit subject exceeding SubjectMaxLen or
// containing line breaks (the notification contract is one stdout line per
// message; an embedded newline would deliver its tail as a bare unprefixed
// line). Empty is valid — it means "derive the subject from the body".
func ValidateSubject(s string) error {
	if strings.ContainsAny(s, "\n\r") {
		return fmt.Errorf("subject must be a single line (put multi-line content in body — receivers recover it via chat-read)")
	}
	if n := utf8.RuneCountInString(s); n > SubjectMaxLen {
		return fmt.Errorf("subject is %d characters; the cap is %d (put the detail in body — receivers recover it via chat-read)", n, SubjectMaxLen)
	}
	return nil
}

// DisplaySubject returns the one-line subject to carry in notification
// lines: the explicit Subject when set, else the body's first line clipped
// to SubjectMaxLen runes (ellipsis-terminated when clipped). Messages
// predating the subject field (or sent via the deprecated `message` alias)
// therefore still render sensibly.
func (m Message) DisplaySubject() string {
	if m.Subject != "" {
		return m.Subject
	}
	line, _, _ := strings.Cut(m.Body, "\n")
	runes := []rune(line)
	if len(runes) <= SubjectMaxLen {
		return line
	}
	return string(runes[:SubjectMaxLen-1]) + "…"
}

// HasMoreThanSubject reports whether the message carries content beyond what
// DisplaySubject shows — the signal for notification lines to append a
// chat-read recovery hint.
func (m Message) HasMoreThanSubject() bool {
	return m.Body != "" && m.Body != m.DisplaySubject()
}

// RecoveryHint is the chat-read invocation that retrieves this message's
// full body — the single owner of the hint string the push path appends
// (the clown wake's result_ref).
func (m Message) RecoveryHint() string {
	return fmt.Sprintf("chat-read from=%s peek=true", m.From)
}
