package tty

import (
	"os"
	"testing"
)

// A pipe is never a terminal, so a prompting path fed by one must not prompt.
func TestTerminalIsFalseForAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	if Terminal(r) || Terminal(w) {
		t.Error("a pipe was reported as a terminal")
	}
}

// A regular file is never a terminal either.
func TestTerminalIsFalseForARegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if Terminal(f) {
		t.Error("a regular file was reported as a terminal")
	}
}
