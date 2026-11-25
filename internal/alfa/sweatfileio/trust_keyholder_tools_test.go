package sweatfileio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeStore points the store root at a temp directory for the test and returns
// it; a path must be under it to count as a store path.
func fakeStore(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prev := KeyholderStoreRoot
	KeyholderStoreRoot = root
	t.Cleanup(func() { KeyholderStoreRoot = prev })
	return root
}

func writeTool(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestCheckKeyholderToolPath(t *testing.T) {
	root := fakeStore(t)
	good := filepath.Join(root, "abc-fibby-holder-0.1.0", "bin", "fibby")
	writeTool(t, good, 0o755)
	noexec := filepath.Join(root, "abc-noexec", "bin", "piggy")
	writeTool(t, noexec, 0o644)
	outside := filepath.Join(t.TempDir(), "outside-fibby")
	writeTool(t, outside, 0o755)
	linkOut := filepath.Join(root, "abc-link", "bin", "fibby")
	if err := os.MkdirAll(filepath.Dir(linkOut), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, linkOut); err != nil {
		t.Fatal(err)
	}
	linkIn := filepath.Join(root, "abc-linkin", "bin", "fibby")
	if err := os.MkdirAll(filepath.Dir(linkIn), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(good, linkIn); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "abc-dir", "bin", "fibby")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, path, want string // want: "" accepts, else a substring of the refusal
	}{
		{"valid", good, ""},
		{"valid through a link that stays in the store", linkIn, ""},
		{"unset", "", "is not set"},
		{"relative", "bin/fibby", "not an absolute path"},
		{"unclean", root + "/abc-fibby-holder-0.1.0/../abc-fibby-holder-0.1.0/bin/fibby", "not clean"},
		{"outside the store", outside, "not under " + root},
		{"a link that leaves the store", linkOut, "leaves " + root},
		{"not executable", noexec, "not executable"},
		{"a directory", dir, "not a regular file"},
		{"missing", filepath.Join(root, "abc-gone", "bin", "fibby"), "cannot be read"},
	}
	for _, c := range cases {
		err := CheckKeyholderToolPath("fibby", c.path)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: refused: %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want a refusal containing %q", c.name, err, c.want)
		}
		if err != nil && !strings.Contains(err.Error(), "[keyholder] fibby") {
			t.Errorf("%s: the refusal does not name the setting: %v", c.name, err)
		}
	}
}

func TestTrustedKeyholderToolsReadOnlyFromLayersAboveTheRepo(t *testing.T) {
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
	write(global, "[keyholder]\nmode = \"on\"\nfibby = \"/nix/store/g-fibby/bin/fibby\"\npiggy = \"/nix/store/g-piggy/bin/piggy\"\n")
	write(repoFile, "[keyholder]\nfibby = \"/tmp/evil/fibby\"\npiggy = \"/tmp/evil/piggy\"\n")
	h, err := LoadHierarchy(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	fibby, piggy, untrusted := TrustedKeyholderTools(h, home, repo)
	if fibby != "/nix/store/g-fibby/bin/fibby" || piggy != "/nix/store/g-piggy/bin/piggy" {
		t.Errorf("tools = %q, %q: a repo layer's paths must never be honoured", fibby, piggy)
	}
	if len(untrusted) != 1 || untrusted[0] != repoFile {
		t.Errorf("untrusted = %v, want the repo file reported (a repo layer that sets only paths too)", untrusted)
	}
	// The mode resolver reports the same layer: one report per layer is enough,
	// but a repo layer that sets only the paths must be reported by it as well.
	if _, un, _ := TrustedKeyholderMode(h, home, repo); len(un) != 1 || un[0] != repoFile {
		t.Errorf("TrustedKeyholderMode untrusted = %v, want the repo file", un)
	}
}

func TestTrustedKeyholderToolsUnsetIsEmpty(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHierarchy(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if f, p, un := TrustedKeyholderTools(h, home, repo); f != "" || p != "" || len(un) != 0 {
		t.Errorf("got (%q, %q, %v), want nothing", f, p, un)
	}
}
