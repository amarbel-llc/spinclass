package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeEnforce(t *testing.T, path string, value bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[profile-rights]\nenforce = false\n"
	if value {
		body = "[profile-rights]\nenforce = true\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTrustedEnforceProfileRightsDefaultsOff(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHierarchy(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if on, untrusted := TrustedEnforceProfileRights(h, home, repo); on || len(untrusted) != 0 {
		t.Errorf("got (%v, %v), want (false, none)", on, untrusted)
	}
}

func TestTrustedEnforceProfileRightsHonoursOnlyLayersAboveRepo(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".config", "spinclass", "sweatfile")
	parent := filepath.Join(home, "work", "sweatfile")
	repoFile := filepath.Join(repo, "sweatfile")
	writeEnforce(t, global, true)
	// A repo layer that tries to switch enforcement off must be ignored.
	writeEnforce(t, repoFile, false)

	resolve := func() (bool, []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		return TrustedEnforceProfileRights(h, home, repo)
	}

	on, ignored := resolve()
	if !on || !reflect.DeepEqual(ignored, []string{repoFile}) {
		t.Errorf("got (%v, %v), want (true, [%s])", on, ignored, repoFile)
	}

	// A false above the repo cannot clear a true: the switch is sticky-on.
	writeEnforce(t, parent, false)
	if on, _ := resolve(); !on {
		t.Errorf("a false ancestor layer cleared the global true")
	}

	// And a repo-layer true cannot turn it on either.
	writeEnforce(t, global, false)
	writeEnforce(t, repoFile, true)
	if on, ignored := resolve(); on || len(ignored) != 1 {
		t.Errorf("repo-layer true honoured: (%v, %v)", on, ignored)
	}
}

func TestTrustedEnforceProfileRightsIgnoresWorktreeLayer(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	wt := filepath.Join(repo, ".worktrees", "x")
	writeEnforce(t, filepath.Join(home, "work", "sweatfile"), true)
	wtFile := filepath.Join(wt, "sweatfile")
	writeEnforce(t, wtFile, false)

	h, err := LoadWorktreeHierarchy(home, repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	on, ignored := TrustedEnforceProfileRights(h, home, repo)
	if !on {
		t.Errorf("worktree layer turned enforcement off")
	}
	if !reflect.DeepEqual(ignored, []string{wtFile}) {
		t.Errorf("ignored = %v, want [%s]", ignored, wtFile)
	}
}

func TestTrustedEnforceProfileRightsIgnoresLabelLayer(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEnforce(t, filepath.Join(home, "work", "sweatfile"), true)

	h, err := LoadHierarchyWithLayer(home, repo, "HEAD:sweatfile",
		[]byte("[profile-rights]\nenforce = false\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	on, untrusted := TrustedEnforceProfileRights(h, home, repo)
	if !on {
		t.Errorf("label layer turned enforcement off")
	}
	if !reflect.DeepEqual(untrusted, []string{"HEAD:sweatfile"}) {
		t.Errorf("untrusted = %v", untrusted)
	}
}

func TestLoadHierarchyMergedNeverCarriesProfileRights(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEnforce(t, filepath.Join(home, ".config", "spinclass", "sweatfile"), true)
	writeEnforce(t, filepath.Join(home, "work", "sweatfile"), true)
	writeEnforce(t, filepath.Join(repo, "sweatfile"), true)

	h, err := LoadHierarchy(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if h.Merged.ProfileRights != nil {
		t.Errorf("Merged carries [profile-rights]: %+v", h.Merged.ProfileRights)
	}
	if on, _ := TrustedEnforceProfileRights(h, home, repo); !on {
		t.Errorf("trusted layers set enforce but it reads off")
	}
}

// An agent picks the target repo path: a repo created outside the operator's
// tree, with a false ancestor layer of its own, must still see the global true.
func TestTrustedEnforceProfileRightsGlobalTrueSurvivesAnyRepoPath(t *testing.T) {
	home := t.TempDir()
	writeEnforce(t, filepath.Join(home, ".config", "spinclass", "sweatfile"), true)

	outside := t.TempDir() // not under home
	repo := filepath.Join(outside, "scratch", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEnforce(t, filepath.Join(outside, "scratch", "sweatfile"), false)
	writeEnforce(t, filepath.Join(outside, "sweatfile"), false)

	h, err := LoadHierarchy(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if on, _ := TrustedEnforceProfileRights(h, home, repo); !on {
		t.Error("a repo path outside home with false ancestor layers escaped the global true")
	}
}

// A parent-scoped true does not cover a repo outside that parent: the caller
// must also consult another context (the driver's); this pins that boundary.
func TestTrustedEnforceProfileRightsParentTrueDoesNotCoverOutsideRepo(t *testing.T) {
	home := t.TempDir()
	writeEnforce(t, filepath.Join(home, "work", "sweatfile"), true)
	inside := filepath.Join(home, "work", "repo")
	outside := filepath.Join(home, "elsewhere", "repo")
	for _, d := range []string{inside, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for repo, want := range map[string]bool{inside: true, outside: false} {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		if on, _ := TrustedEnforceProfileRights(h, home, repo); on != want {
			t.Errorf("repo %s: got %v, want %v", repo, on, want)
		}
	}
}

func TestTrustedEnforceProfileRightsViaSymlinkedRepo(t *testing.T) {
	home := t.TempDir()
	realWork := filepath.Join(home, "real", "work")
	realRepo := filepath.Join(realWork, "repo")
	if err := os.MkdirAll(realRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realWork, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	writeEnforce(t, filepath.Join(realWork, "sweatfile"), true)
	writeEnforce(t, filepath.Join(realRepo, "sweatfile"), false)

	linkRepo := filepath.Join(home, "link", "repo")
	h, err := LoadHierarchy(home, linkRepo)
	if err != nil {
		t.Fatal(err)
	}
	on, ignored := TrustedEnforceProfileRights(h, home, linkRepo)
	if !on {
		t.Errorf("repo layer reached through a symlink turned enforcement off")
	}
	if len(ignored) == 0 {
		t.Errorf("repo-layer key not reported as ignored")
	}
	for _, p := range ignored {
		if filepath.Base(filepath.Dir(p)) != "repo" {
			t.Errorf("non-repo layer reported ignored: %s", p)
		}
	}
}
