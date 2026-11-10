package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// [host-queue] record-costs (#424 step 0) is trusted-layers-only: read only from
// layers above the repo, last trusted layer wins, on when unset. A repo layer
// cannot turn the operator's cost tracking off for its own gates.
func TestTrustedHostQueueRecordCosts(t *testing.T) {
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
		return TrustedHostQueueRecordCosts(h, home, repo)
	}

	if record, untrusted := resolve(); !record || len(untrusted) != 0 {
		t.Errorf("unset: got (%v, %v), want (true, none)", record, untrusted)
	}
	write(repoFile, "[host-queue]\nrecord-costs = false\n")
	if record, untrusted := resolve(); !record || !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("repo layer: got (%v, %v), want (true, [%s])", record, untrusted, repoFile)
	}
	write(global, "[host-queue]\nrecord-costs = false\n")
	if record, _ := resolve(); record {
		t.Error("a global false is not honoured")
	}
	write(parent, "[host-queue]\nrecord-costs = true\n")
	if record, _ := resolve(); !record {
		t.Error("the last trusted layer should win")
	}
}
