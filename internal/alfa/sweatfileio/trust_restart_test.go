package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// [session-entry] restart (#419) follows the url-resolver rule: read only from
// layers above the repo, the last trusted layer that sets it wins, an explicit
// [] clears it, and Hierarchy.Merged never carries it.
func TestTrustedRestartHandoff(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".config", "spinclass", "sweatfile")
	parent := filepath.Join(home, "work", "sweatfile")
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
	load := func() (argv, untrusted []string, merged []string) {
		t.Helper()
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		argv, untrusted = TrustedRestartHandoff(h, home, repo)
		if h.Merged.SessionEntry != nil {
			merged = h.Merged.SessionEntry.Restart
		}
		return argv, untrusted, merged
	}

	if argv, untrusted, _ := load(); argv != nil || len(untrusted) != 0 {
		t.Errorf("unset: got (%v, %v), want (nil, none)", argv, untrusted)
	}

	write(repoFile, "[session-entry]\nrestart = [\"evil\", \"{principal}\"]\n")
	if argv, untrusted, merged := load(); argv != nil ||
		!reflect.DeepEqual(untrusted, []string{repoFile}) || merged != nil {
		t.Errorf("repo layer: got (%v, %v, merged %v), want (nil, [%s], nil)", argv, untrusted, merged, repoFile)
	}

	want := []string{"clown", "restart", "--session", "{principal}"}
	write(global, "[session-entry]\nrestart = [\"clown\", \"restart\", \"--session\", \"{principal}\"]\n")
	argv, untrusted, merged := load()
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("a global value is not honoured: got %v, want %v", argv, want)
	}
	if !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("the repo layer must still be reported untrusted: %v", untrusted)
	}
	if merged != nil {
		t.Errorf("Hierarchy.Merged carries restart: %v", merged)
	}

	write(parent, "[session-entry]\nrestart = [\"other\", \"restart\"]\n")
	if argv, _, _ := load(); !reflect.DeepEqual(argv, []string{"other", "restart"}) {
		t.Errorf("the last trusted layer should win: got %v", argv)
	}

	write(parent, "[session-entry]\nrestart = []\n")
	if argv, _, _ := load(); len(argv) != 0 {
		t.Errorf("an explicit [] should clear the inherited value: got %v", argv)
	}
}
