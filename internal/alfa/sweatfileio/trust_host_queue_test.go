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

// The admission keys (#424 step 4) follow the same rule, key by key: a repo
// layer cannot enable, disable or loosen the queue, and a nearer trusted layer
// (a parent directory above one test repo) overrides the global one per key.
// That parent layer is how the queue is tried on one repo without touching the
// global sweatfile.
func TestTrustedHostQueue(t *testing.T) {
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
	resolve := func() (bool, int, string, []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		hq, untrusted := TrustedHostQueue(h, home, repo)
		enable, gates, settle := false, 0, ""
		if hq.Enable != nil {
			enable = *hq.Enable
		}
		if hq.MaxGates != nil {
			gates = *hq.MaxGates
		}
		if hq.Settle != nil {
			settle = *hq.Settle
		}
		return enable, gates, settle, untrusted
	}

	if on, _, _, untrusted := resolve(); on || len(untrusted) != 0 {
		t.Errorf("unset: got (%v, %v), want off and nothing ignored", on, untrusted)
	}
	write(repoFile, "[host-queue]\nenable = true\nmax-gates = 9\n")
	if on, gates, _, untrusted := resolve(); on || gates != 0 || !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("repo layer: got (%v, %d, %v), want ignored", on, gates, untrusted)
	}
	write(global, "[host-queue]\nmax-gates = 2\nsettle = \"90s\"\n")
	write(parent, "[host-queue]\nenable = true\nsettle = \"30s\"\n")
	on, gates, settle, _ := resolve()
	if !on || gates != 2 || settle != "30s" {
		t.Errorf("trusted layers: got (%v, %d, %q), want (true, 2 from global, 30s from parent)", on, gates, settle)
	}
}
