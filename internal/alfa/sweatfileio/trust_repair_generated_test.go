package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// [hooks] repair-generated: protect and fail only narrow what repair may do,
// so every layer may set them; allow widens it, so only a layer above the repo
// may. The last honoured layer wins, and protect is the default.
func TestTrustedRepairGenerated(t *testing.T) {
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
	remove := func(path string) {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func() (sweatfile.RepairGeneratedMode, []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		return TrustedRepairGenerated(h, home, repo)
	}

	if mode, untrusted := resolve(); mode != sweatfile.RepairGeneratedProtect || len(untrusted) != 0 {
		t.Errorf("unset: got (%q, %v), want (protect, none)", mode, untrusted)
	}

	write(repoFile, "[hooks]\nrepair-generated = \"allow\"\n")
	if mode, untrusted := resolve(); mode != sweatfile.RepairGeneratedProtect || !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("repo-layer allow: got (%q, %v), want (protect, [%s])", mode, untrusted, repoFile)
	}

	write(global, "[hooks]\nrepair-generated = \"allow\"\n")
	if mode, untrusted := resolve(); mode != sweatfile.RepairGeneratedAllow || !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("global allow under a repo-layer allow: got (%q, %v), want (allow, [%s])", mode, untrusted, repoFile)
	}

	write(repoFile, "[hooks]\nrepair-generated = \"fail\"\n")
	if mode, untrusted := resolve(); mode != sweatfile.RepairGeneratedFail || len(untrusted) != 0 {
		t.Errorf("repo-layer fail over a global allow: got (%q, %v), want (fail, none)", mode, untrusted)
	}

	write(repoFile, "[hooks]\nrepair-generated = \"protect\"\n")
	write(global, "[hooks]\nrepair-generated = \"fail\"\n")
	if mode, _ := resolve(); mode != sweatfile.RepairGeneratedProtect {
		t.Errorf("repo-layer protect over a global fail: got %q, want protect (the last honoured layer wins)", mode)
	}

	remove(repoFile)
	write(parent, "[hooks]\nrepair-generated = \"allow\"\n")
	if mode, _ := resolve(); mode != sweatfile.RepairGeneratedAllow {
		t.Errorf("parent-dir allow over a global fail: got %q, want allow", mode)
	}

	write(parent, "[hooks]\nrepair-generated = \"sometimes\"\n")
	if mode, _ := resolve(); mode != sweatfile.RepairGeneratedProtect {
		t.Errorf("unknown value: got %q, want protect", mode)
	}
}
