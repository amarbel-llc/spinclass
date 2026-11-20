package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// [session-entry] start-handoff, the command start-child-session runs to
// relaunch a stopped session, follows the restart hand-off's rule: read only
// from layers above the repo, the last trusted layer wins, and
// Hierarchy.Merged never carries it.
func TestTrustedStartHandoff(t *testing.T) {
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
	load := func() (argv, untrusted, merged []string) {
		t.Helper()
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		argv, untrusted = TrustedStartHandoff(h, home, repo)
		if h.Merged.SessionEntry != nil {
			merged = h.Merged.SessionEntry.StartHandoff
		}
		return argv, untrusted, merged
	}

	write(repoFile, "[session-entry]\nstart-handoff = [\"evil\"]\n")
	if argv, untrusted, merged := load(); argv != nil || !reflect.DeepEqual(untrusted, []string{repoFile}) || merged != nil {
		t.Errorf("repo layer: got (%v, %v, merged %v), want (nil, [%s], nil)", argv, untrusted, merged, repoFile)
	}

	want := []string{"env", "CLOWN_SESSION_ID={principal}", "clown", "--", "--resume", "{principal}", "{message}"}
	write(global, "[session-entry]\nstart-handoff = [\"env\", \"CLOWN_SESSION_ID={principal}\", \"clown\", \"--\", \"--resume\", \"{principal}\", \"{message}\"]\n")
	argv, untrusted, merged := load()
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("a global value is not honoured: got %v, want %v", argv, want)
	}
	if !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("the repo layer must still be reported untrusted: %v", untrusted)
	}
	if merged != nil {
		t.Errorf("Hierarchy.Merged carries start-handoff: %v", merged)
	}
}
