package chat

import (
	"strings"
	"testing"
)

func TestValidateSubjectAcceptsShort(t *testing.T) {
	if err := ValidateSubject("fix landed, please rebase"); err != nil {
		t.Fatalf("short subject: %v", err)
	}
}

func TestValidateSubjectAcceptsEmpty(t *testing.T) {
	// Empty means "derive from body" — validated as absent, not as too short.
	if err := ValidateSubject(""); err != nil {
		t.Fatalf("empty subject: %v", err)
	}
}

func TestValidateSubjectRejectsNewlines(t *testing.T) {
	// A newline in the subject would split the single-line notification
	// contract: the monitor writes one stdout line per message, and the
	// fragment after the newline would arrive as a bare unprefixed line.
	for _, s := range []string{"two\nlines", "trailing\n", "carriage\rreturn"} {
		if err := ValidateSubject(s); err == nil {
			t.Fatalf("subject %q with line break: want error, got nil", s)
		}
	}
}

func TestValidateSubjectRejectsOverCap(t *testing.T) {
	if err := ValidateSubject(strings.Repeat("x", SubjectMaxLen+1)); err == nil {
		t.Fatal("over-cap subject: want error, got nil")
	}
}

func TestValidateSubjectCountsRunesNotBytes(t *testing.T) {
	// SubjectMaxLen multibyte runes are exactly at the cap, not over it.
	if err := ValidateSubject(strings.Repeat("✅", SubjectMaxLen)); err != nil {
		t.Fatalf("cap-length multibyte subject: %v", err)
	}
}

func TestDisplaySubjectPrefersExplicit(t *testing.T) {
	m := Message{Subject: "the subject", Body: "the body\nwith lines"}
	if got := m.DisplaySubject(); got != "the subject" {
		t.Fatalf("got %q, want explicit subject", got)
	}
}

func TestDisplaySubjectDerivesFirstBodyLine(t *testing.T) {
	m := Message{Body: "first line summary\nsecond line\nthird"}
	if got := m.DisplaySubject(); got != "first line summary" {
		t.Fatalf("got %q, want first body line", got)
	}
}

func TestDisplaySubjectClipsLongDerivation(t *testing.T) {
	long := strings.Repeat("a", SubjectMaxLen+50)
	m := Message{Body: long}
	got := m.DisplaySubject()
	if runes := []rune(got); len(runes) != SubjectMaxLen {
		t.Fatalf("clipped length: got %d runes, want %d", len(runes), SubjectMaxLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("clipped derivation missing ellipsis: %q", got)
	}
}

func TestDisplaySubjectEmptyMessage(t *testing.T) {
	m := Message{}
	if got := m.DisplaySubject(); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestHasMoreThanSubject(t *testing.T) {
	cases := []struct {
		name string
		m    Message
		want bool
	}{
		{"subject only", Message{Subject: "s"}, false},
		{"subject plus body", Message{Subject: "s", Body: "long body"}, true},
		{"body fully shown as derived subject", Message{Body: "one short line"}, false},
		{"multi-line body behind derived subject", Message{Body: "line one\nline two"}, true},
	}
	for _, tc := range cases {
		if got := tc.m.HasMoreThanSubject(); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
