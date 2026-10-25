package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeResolver(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[auth]\nurl-resolver = \"" + value + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseURLResolver(t *testing.T) {
	doc, err := Parse([]byte("[auth]\nurl-resolver = \"smith repo resolve {origin} --output json\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := doc.Data().Auth
	if got == nil || got.URLResolver == nil || *got.URLResolver != "smith repo resolve {origin} --output json" {
		t.Fatalf("url-resolver not parsed: %+v", got)
	}
}

func TestTrustedURLResolverHonoursOnlyLayersAboveRepo(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".config", "spinclass", "sweatfile")
	parent := filepath.Join(home, "work", "sweatfile")
	repoFile := filepath.Join(repo, "sweatfile")
	writeResolver(t, global, "global")
	writeResolver(t, parent, "parent")
	writeResolver(t, repoFile, "repo")

	resolve := func() (string, []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		return TrustedURLResolver(h, home, repo)
	}

	cmd, ignored := resolve()
	if cmd != "parent" || !reflect.DeepEqual(ignored, []string{repoFile}) {
		t.Errorf("got (%q, %v), want (parent, [%s])", cmd, ignored, repoFile)
	}

	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if cmd, _ := resolve(); cmd != "global" {
		t.Errorf("after removing parent: got %q, want global", cmd)
	}

	writeResolver(t, parent, "")
	if cmd, _ := resolve(); cmd != "" {
		t.Errorf("explicit empty parent should clear: got %q", cmd)
	}
}

func TestTrustedSpawnProfileHonoursOnlyLayersAboveRepo(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	wt := filepath.Join(repo, ".worktrees", "x")
	write := func(path, profile string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("[session-entry]\nspawn-profile = \""+profile+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repoFile, wtFile := filepath.Join(repo, "sweatfile"), filepath.Join(wt, "sweatfile")
	linkedWorktree(t, repo, wt)
	write(filepath.Join(home, ".config", "spinclass", "sweatfile"), "global")
	write(filepath.Join(home, "work", "sweatfile"), "parent")
	write(repoFile, "repo")
	write(wtFile, "wt")

	// LoadHierarchy rooted at the worktree resolves to the main checkout, so the
	// worktree's own file is not even a source to be ignored.
	h, err := LoadHierarchy(home, wt)
	if err != nil {
		t.Fatal(err)
	}
	got, ignored := TrustedSpawnProfile(h, home, repo)
	if got != "parent" || !reflect.DeepEqual(ignored, []string{repoFile}) {
		t.Errorf("got (%q, %v), want (parent, [%s])", got, ignored, repoFile)
	}
}

func TestTrustedURLResolverIgnoresWorktreeLayer(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	wt := filepath.Join(repo, ".worktrees", "x")
	writeResolver(t, filepath.Join(home, "work", "sweatfile"), "parent")
	wtFile := filepath.Join(wt, "sweatfile")
	writeResolver(t, wtFile, "wt")
	linkedWorktree(t, repo, wt)

	h, err := LoadHierarchy(home, wt)
	if err != nil {
		t.Fatal(err)
	}
	cmd, ignored := TrustedURLResolver(h, home, repo)
	if cmd != "parent" {
		t.Errorf("got %q, want parent", cmd)
	}
	if len(ignored) != 0 {
		t.Errorf("ignored = %v, want none: the worktree file is not loaded at all", ignored)
	}
}

func TestLoadHierarchyMergedNeverCarriesURLResolver(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeResolver(t, filepath.Join(home, ".config", "spinclass", "sweatfile"), "global")
	writeResolver(t, filepath.Join(home, "work", "sweatfile"), "parent")

	h, err := LoadHierarchy(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if h.Merged.Auth != nil && h.Merged.Auth.URLResolver != nil {
		t.Errorf("Merged carries url-resolver %q", *h.Merged.Auth.URLResolver)
	}
	if cmd, _ := TrustedURLResolver(h, home, repo); cmd != "parent" {
		t.Errorf("got %q, want parent", cmd)
	}
}

func TestTrustedURLResolverIgnoresLabelLayer(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeResolver(t, filepath.Join(home, "work", "sweatfile"), "parent")

	h, err := LoadHierarchyWithLayer(home, repo, "HEAD:sweatfile",
		[]byte("[auth]\nurl-resolver = \"label\"\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	cmd, untrusted := TrustedURLResolver(h, home, repo)
	if cmd != "parent" {
		t.Errorf("got %q, want parent", cmd)
	}
	if !reflect.DeepEqual(untrusted, []string{"HEAD:sweatfile"}) {
		t.Errorf("untrusted = %v", untrusted)
	}
}

func TestTrustedURLResolverViaSymlinkedRepo(t *testing.T) {
	home := t.TempDir()
	realWork := filepath.Join(home, "real", "work")
	realRepo := filepath.Join(realWork, "repo")
	if err := os.MkdirAll(realRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realWork, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	writeResolver(t, filepath.Join(realWork, "sweatfile"), "parent")
	writeResolver(t, filepath.Join(realRepo, "sweatfile"), "repo")

	linkRepo := filepath.Join(home, "link", "repo")
	h, err := LoadHierarchy(home, linkRepo)
	if err != nil {
		t.Fatal(err)
	}
	cmd, ignored := TrustedURLResolver(h, home, linkRepo)
	if cmd != "parent" {
		t.Errorf("got %q, want parent", cmd)
	}
	if len(ignored) == 0 {
		t.Errorf("repo-layer resolver not reported as ignored")
	}
	for _, p := range ignored {
		if filepath.Base(filepath.Dir(p)) != "repo" {
			t.Errorf("non-repo layer reported ignored: %s", p)
		}
	}
}
