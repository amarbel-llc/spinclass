package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TEMPORARY (spinclass#384 stopgap): [[permissions.spawn-allow]] is read only
// from layers above the repo, appended across them in order; a repo-layer list
// is reported untrusted and never honoured; an invalid entry comes back with a
// Problem so the caller ignores it.
func TestTrustedSpawnAllow(t *testing.T) {
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
	resolve := func() (principals []string, problems int, untrusted []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		entries, untrusted := TrustedSpawnAllow(h, home, repo)
		for _, e := range entries {
			if e.Problem != "" {
				problems++
				continue
			}
			principals = append(principals, e.Principal+e.Session)
		}
		return principals, problems, untrusted
	}
	entry := func(body string) string { return "[permissions]\n\n[[permissions.spawn-allow]]\n" + body }

	if p, n, u := resolve(); len(p)+n+len(u) != 0 {
		t.Errorf("unset: got (%v, %d, %v), want nothing", p, n, u)
	}
	write(repoFile, entry("principal = \"repo-layer\"\n"))
	if p, _, u := resolve(); len(p) != 0 || !reflect.DeepEqual(u, []string{repoFile}) {
		t.Errorf("repo layer: got (%v, %v), want nothing honoured and the file untrusted", p, u)
	}
	write(global, entry("principal = \"first\"\nmax-workers = 3\n")+"\n[[permissions.spawn-allow]]\nsession = \"circus/coordinator\"\n")
	write(parent, entry("principal = \"second\"\n")+"\n[[permissions.spawn-allow]]\nprincipal = \"a\"\nsession = \"b/c\"\n")
	p, problems, _ := resolve()
	if want := []string{"first", "circus/coordinator", "second"}; !reflect.DeepEqual(p, want) {
		t.Errorf("honoured = %v, want %v (global then parent, in order, repo layer excluded)", p, want)
	}
	if problems != 1 {
		t.Errorf("problems = %d, want the one entry naming both principal and session", problems)
	}
}
