package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTrustedStoreIdentityHonoursOnlyLayersAboveRepo(t *testing.T) {
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
	resolve := func() ([4]string, []string) {
		t.Helper()
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		an, ae, cn, ce, untrusted := TrustedStoreIdentity(h, home, repo)
		return [4]string{an, ae, cn, ce}, untrusted
	}

	if got, untrusted := resolve(); got != [4]string{} || len(untrusted) != 0 {
		t.Errorf("unset: got (%v, %v)", got, untrusted)
	}

	write(repoFile, "[store]\nauthor-name = \"Repo\"\nauthor-email = \"repo@x\"\n")
	if got, untrusted := resolve(); got != [4]string{} || !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("repo layer: got (%v, %v), want (none, [%s])", got, untrusted, repoFile)
	}

	write(global, "[store]\nauthor-name = \"Global\"\nauthor-email = \"global@x\"\n")
	if got, _ := resolve(); got != [4]string{"Global", "global@x", "Global", "global@x"} {
		t.Errorf("global: got %v", got)
	}

	write(parent, "[store]\nauthor-name = \"Parent\"\ncommitter-email = \"bot@x\"\n")
	want := [4]string{"Parent", "global@x", "Parent", "bot@x"}
	if got, untrusted := resolve(); got != want || !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("per-key last trusted layer: got (%v, %v), want %v", got, untrusted, want)
	}
}

func TestLoadHierarchyMergedNeverCarriesStore(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".config", "spinclass", "sweatfile")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("[store]\nauthor-name = \"Global\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHierarchy(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if h.Merged.Store != nil {
		t.Errorf("Merged carries [store]: %+v", h.Merged.Store)
	}
}
