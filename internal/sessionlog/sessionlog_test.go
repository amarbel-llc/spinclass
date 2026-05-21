package sessionlog

import (
	"os"
	"strings"
	"testing"
)

func TestOpenWritesToXDGLogHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_LOG_HOME", dir)
	t.Cleanup(func() { _ = Close() })

	if err := Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}

	p := Path()
	if p == "" {
		t.Fatal("Path empty after Open")
	}
	if !strings.HasPrefix(p, dir) {
		t.Errorf("log path %q does not live under %q", p, dir)
	}

	Infof("hello %s", "world")
	Errorf("boom")

	if err := Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	s := string(body)
	for _, want := range []string{"sessionlog.open", "INFO ", "hello world", "ERROR ", "boom"} {
		if !strings.Contains(s, want) {
			t.Errorf("log missing %q; got:\n%s", want, s)
		}
	}

	// Caller capture: log lines should reference this test file, not
	// sessionlog.go. That's the whole point of runtime.Caller(2) —
	// "who called Infof" beats "Infof itself" when debugging.
	if !strings.Contains(s, "sessionlog_test.go:") {
		t.Errorf("log missing caller file:line tag; got:\n%s", s)
	}
}

func TestEmitBeforeOpenIsNoop(t *testing.T) {
	t.Cleanup(func() { _ = Close() })
	Infof("ignored")
	Errorf("ignored")
	if Path() != "" {
		t.Errorf("Path returned non-empty before Open: %q", Path())
	}
}

func TestFallbackWhenXDGLogHomeUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_LOG_HOME", "")
	t.Cleanup(func() { _ = Close() })

	if err := Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}

	p := Path()
	want := home + "/.local/log/spinclass/lifecycle.log"
	if p != want {
		t.Errorf("fallback path: got %q want %q", p, want)
	}
}

func TestDoubleCloseAndDoubleOpen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_LOG_HOME", dir)
	t.Cleanup(func() { _ = Close() })

	if err := Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	first := Path()

	if err := Open(); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if Path() != first {
		t.Errorf("second Open changed path: %q -> %q", first, Path())
	}

	if err := Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if Path() != "" {
		t.Errorf("Path non-empty after Close: %q", Path())
	}
}
