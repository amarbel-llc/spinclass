package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// [health] busy-bound is read only from layers above the repo, the last valid
// trusted value wins, and a bad value never disables detection.
func TestTrustedHealthBusyBound(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".config", "spinclass", "sweatfile")
	repoFile := filepath.Join(repo, "sweatfile")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func() (time.Duration, []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		return TrustedHealthBusyBound(h, home, repo)
	}

	if d, u := resolve(); d != 60*time.Minute || len(u) != 0 {
		t.Errorf("unset: got (%v, %v), want (60m, none)", d, u)
	}
	write(repoFile, "[health]\nbusy-bound = \"24h\"\n")
	if d, u := resolve(); d != 60*time.Minute || !reflect.DeepEqual(u, []string{repoFile}) {
		t.Errorf("repo layer: got (%v, %v), want (60m, [%s])", d, u, repoFile)
	}
	write(global, "[health]\nbusy-bound = \"30m\"\n")
	if d, _ := resolve(); d != 30*time.Minute {
		t.Errorf("global 30m: got %v", d)
	}
	write(global, "[health]\nbusy-bound = \"soon\"\n")
	if d, _ := resolve(); d != 60*time.Minute {
		t.Errorf("unparseable: got %v, want the default", d)
	}
	write(global, "[health]\nbusy-bound = \"-5m\"\n")
	if d, _ := resolve(); d != 60*time.Minute {
		t.Errorf("non-positive: got %v, want the default", d)
	}
}
