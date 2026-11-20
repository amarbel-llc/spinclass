package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// [sysprompt] principles-file names a file whose text goes into every
// session's system prompt, so a value the agent can write into its repo's own
// sweatfile must not choose it: read only from layers above the repo, the last
// trusted layer wins, an explicit "" clears, and Hierarchy.Merged never
// carries it.
func TestTrustedPrinciplesFile(t *testing.T) {
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
	load := func() (path string, untrusted []string, merged *string) {
		t.Helper()
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		path, untrusted = TrustedPrinciplesFile(h, home, repo)
		if h.Merged.Sysprompt != nil {
			merged = h.Merged.Sysprompt.PrinciplesFile
		}
		return path, untrusted, merged
	}

	write(repoFile, "[sysprompt]\nprinciples-file = \"/evil.md\"\n")
	if path, untrusted, merged := load(); path != "" || !reflect.DeepEqual(untrusted, []string{repoFile}) || merged != nil {
		t.Errorf("repo layer: got (%q, %v, merged %v), want (\"\", [%s], nil)", path, untrusted, merged, repoFile)
	}

	write(global, "[sysprompt]\nprinciples-file = \"~/.config/circus/principles.md\"\n")
	if path, _, merged := load(); path != "~/.config/circus/principles.md" || merged != nil {
		t.Errorf("global: got (%q, merged %v), want the global value and nothing merged", path, merged)
	}

	write(parent, "[sysprompt]\nprinciples-file = \"/other.md\"\n")
	if path, _, _ := load(); path != "/other.md" {
		t.Errorf("the last trusted layer should win: got %q", path)
	}

	write(parent, "[sysprompt]\nprinciples-file = \"\"\n")
	if path, _, _ := load(); path != "" {
		t.Errorf("an explicit \"\" should clear the inherited value: got %q", path)
	}
}
