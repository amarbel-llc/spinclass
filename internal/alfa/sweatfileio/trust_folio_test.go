package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// [folio] (spinclass#438): the allow lists (read, write) and unmatched are
// honoured only from layers above the repo; allows append across trusted
// layers and an explicit [] in a nearer trusted layer clears the ones above;
// unmatched is a scalar, the last trusted layer wins. Denies narrow, so they
// are honoured from EVERY layer (an agent may restrict itself), appended; only
// a trusted layer's [] clears them. Layers that set an allow or unmatched
// without being trusted are reported in untrusted and ignored.
func TestTrustedFolio(t *testing.T) {
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
	resolve := func() (FolioPolicy, []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		return TrustedFolio(h, home, repo)
	}

	if p, untrusted := resolve(); p.Configured || len(untrusted) != 0 {
		t.Errorf("unset: got (%+v, %v), want unconfigured and no untrusted", p, untrusted)
	}

	write(repoFile, "[folio]\nread = [\"spinclass:worktrees\"]\ndeny = [\"pwd:secret\"]\nunmatched = \"deny\"\n")
	p, untrusted := resolve()
	if p.Configured {
		t.Errorf("a repo layer alone must not configure the policy: %+v", p)
	}
	if len(p.Read) != 0 || p.Unmatched != "" {
		t.Errorf("repo-layer allows or unmatched honoured: %+v", p)
	}
	if !reflect.DeepEqual(p.Deny, []string{"pwd:secret"}) {
		t.Errorf("a repo-layer deny must be honoured, got %v", p.Deny)
	}
	if !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("untrusted = %v, want [%s]", untrusted, repoFile)
	}

	write(global, "[folio]\nread = [\"pwd:\", \"nix-store:\"]\nwrite = [\"pwd:\"]\ndeny = [\"env:XDG_STATE_HOME:circus\"]\nunmatched = \"ask\"\n")
	write(parent, "[folio]\nread = [\"spinclass:children\"]\nunmatched = \"deny\"\n")
	p, _ = resolve()
	if !p.Configured {
		t.Error("a global stanza configures the policy")
	}
	if !reflect.DeepEqual(p.Read, []string{"pwd:", "nix-store:", "spinclass:children"}) {
		t.Errorf("read should append across trusted layers, got %v", p.Read)
	}
	if !reflect.DeepEqual(p.Write, []string{"pwd:"}) {
		t.Errorf("write = %v", p.Write)
	}
	if !reflect.DeepEqual(p.Deny, []string{"env:XDG_STATE_HOME:circus", "pwd:secret"}) {
		t.Errorf("deny should append across every layer, trusted first, got %v", p.Deny)
	}
	if p.Unmatched != "deny" {
		t.Errorf("unmatched: the last trusted layer wins, got %q", p.Unmatched)
	}

	write(parent, "[folio]\nread = []\ndeny = []\n")
	p, _ = resolve()
	if len(p.Read) != 0 {
		t.Errorf("[] in a nearer trusted layer should clear read, got %v", p.Read)
	}
	if !reflect.DeepEqual(p.Deny, []string{"pwd:secret"}) {
		t.Errorf("[] in a trusted layer clears the denies above it, the repo layer still appends: got %v", p.Deny)
	}
	if p.Unmatched != "ask" {
		t.Errorf("unmatched should inherit the global value when the parent drops it, got %q", p.Unmatched)
	}

	write(parent, "[folio]\ndeny = [\"pwd:a\"]\n")
	write(repoFile, "[folio]\ndeny = []\n")
	if p, _ := resolve(); !reflect.DeepEqual(p.Deny, []string{"env:XDG_STATE_HOME:circus", "pwd:a"}) {
		t.Errorf("an untrusted [] must not clear denies, got %v", p.Deny)
	}
}
