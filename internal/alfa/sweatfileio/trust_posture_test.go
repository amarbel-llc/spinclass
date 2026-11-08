package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// [session-entry] allow-posture-widening follows the restart rule: read only
// from layers above the repo, the nearest trusted layer that sets it wins, a
// repo value is reported untrusted and never honoured, and Hierarchy.Merged
// never carries it (spinclass#342).
func TestTrustedAllowPostureWidening(t *testing.T) {
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
	load := func() (allowed bool, untrusted []string, merged *bool) {
		t.Helper()
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		allowed, untrusted = TrustedAllowPostureWidening(h, home, repo)
		if h.Merged.SessionEntry != nil {
			merged = h.Merged.SessionEntry.AllowPostureWidening
		}
		return allowed, untrusted, merged
	}

	if allowed, untrusted, _ := load(); allowed || len(untrusted) != 0 {
		t.Errorf("unset: got (%v, %v), want (false, none)", allowed, untrusted)
	}

	write(repoFile, "[session-entry]\nallow-posture-widening = true\n")
	if allowed, untrusted, merged := load(); allowed ||
		!reflect.DeepEqual(untrusted, []string{repoFile}) || merged != nil {
		t.Errorf("repo layer: got (%v, %v, merged %v), want (false, [%s], nil)", allowed, untrusted, merged, repoFile)
	}

	write(global, "[session-entry]\nallow-posture-widening = true\n")
	allowed, untrusted, merged := load()
	if !allowed {
		t.Error("a global true is not honoured")
	}
	if !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("the repo layer must still be reported untrusted: %v", untrusted)
	}
	if merged != nil {
		t.Errorf("Hierarchy.Merged carries allow-posture-widening: %v", *merged)
	}

	write(parent, "[session-entry]\nallow-posture-widening = false\n")
	if allowed, _, _ := load(); allowed {
		t.Error("the nearer trusted layer should win: a parent false turns it off")
	}
}
