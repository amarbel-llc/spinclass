package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// [session-entry] unattended-permission-mode follows the restart rule: read
// only from layers above the repo, the nearest trusted layer that sets it wins,
// and Hierarchy.Merged never carries it.
func TestTrustedUnattendedPermissionMode(t *testing.T) {
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
	load := func() (mode string, untrusted []string, merged *string) {
		t.Helper()
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		mode, untrusted = TrustedUnattendedPermissionMode(h, home, repo)
		if h.Merged.SessionEntry != nil {
			merged = h.Merged.SessionEntry.UnattendedPermissionMode
		}
		return mode, untrusted, merged
	}

	if mode, untrusted, _ := load(); mode != "" || len(untrusted) != 0 {
		t.Errorf("unset: got (%q, %v), want (\"\", none)", mode, untrusted)
	}

	write(repoFile, "[session-entry]\nunattended-permission-mode = \"auto\"\n")
	if mode, untrusted, merged := load(); mode != "" ||
		!reflect.DeepEqual(untrusted, []string{repoFile}) || merged != nil {
		t.Errorf("repo layer: got (%q, %v, merged %v), want (\"\", [%s], nil)", mode, untrusted, merged, repoFile)
	}

	write(global, "[session-entry]\nunattended-permission-mode = \"dontAsk\"\n")
	mode, untrusted, merged := load()
	if mode != "dontAsk" {
		t.Errorf("a global value is not honoured: got %q, want dontAsk", mode)
	}
	if !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("the repo layer must still be reported untrusted: %v", untrusted)
	}
	if merged != nil {
		t.Errorf("Hierarchy.Merged carries unattended-permission-mode: %v", *merged)
	}

	write(parent, "[session-entry]\nunattended-permission-mode = \"auto\"\n")
	if mode, _, _ := load(); mode != "auto" {
		t.Errorf("the nearer trusted layer should win: got %q, want auto", mode)
	}
}
