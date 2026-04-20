package servelog

import (
	"os"
	"strings"
	"testing"
)

func TestOpenWritesToXDGStateHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
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
	for _, want := range []string{"servelog.open", "INFO hello world", "ERROR boom", "servelog.close"} {
		if !strings.Contains(s, want) {
			t.Errorf("log missing %q; got:\n%s", want, s)
		}
	}
}

func TestInfofBeforeOpenIsNoop(t *testing.T) {
	t.Cleanup(func() { _ = Close() })
	Infof("ignored")
	Errorf("ignored")
	if Path() != "" {
		t.Errorf("Path returned non-empty before Open: %q", Path())
	}
}

func TestDoubleCloseAndDoubleOpen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
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
