package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// [hooks] allow-merge-commits (#468) follows the allow-self-close rule: read
// only from layers above the repo, last trusted layer wins, off when unset. A
// repo layer cannot switch it on, so a session cannot let its own merge commit
// past the merge's refusal.
func TestTrustedAllowMergeCommits(t *testing.T) {
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
	resolve := func() (bool, []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		return TrustedAllowMergeCommits(h, home, repo)
	}

	if allow, untrusted := resolve(); allow || len(untrusted) != 0 {
		t.Errorf("unset: got (%v, %v), want (false, none)", allow, untrusted)
	}
	write(repoFile, "[hooks]\nallow-merge-commits = true\n")
	if allow, untrusted := resolve(); allow || !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("repo layer: got (%v, %v), want (false, [%s])", allow, untrusted, repoFile)
	}
	write(global, "[hooks]\nallow-merge-commits = true\n")
	if allow, _ := resolve(); !allow {
		t.Error("a global true is not honoured")
	}
	write(parent, "[hooks]\nallow-merge-commits = false\n")
	if allow, _ := resolve(); allow {
		t.Error("the last trusted layer should win")
	}
}
