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

func TestTrustedConfinement(t *testing.T) {
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
	resolve := func() (bool, []string, []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		p, egress, untrusted := TrustedConfinement(h, home, repo)
		return p.DefaultConfines(), egress, untrusted
	}

	launch, egress, untrusted := resolve()
	if launch || len(untrusted) != 0 || egress != nil {
		t.Errorf("defaults: got (%v, %v, %v), want egress nil so clown's default applies", launch, egress, untrusted)
	}

	write(global, "[confinement]\nlaunch-unit = true\negress-allow = [\"a:1\"]\n")
	write(parent, "[confinement]\nlaunch-unit = false\negress-allow = [\"b:2\"]\n")
	write(repoFile, "[confinement]\nlaunch-unit = false\negress-allow = [\"evil:9\"]\n")
	launch, egress, untrusted = resolve()
	if !launch {
		t.Error("a false layer cleared launch-unit")
	}
	if !reflect.DeepEqual(egress, []string{"b:2"}) {
		t.Errorf("egress = %v, want the last trusted layer [b:2]", egress)
	}
	if !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("untrusted = %v, want [%s]", untrusted, repoFile)
	}

	write(parent, "[confinement]\negress-allow = []\n")
	if _, egress, _ := resolve(); egress == nil || len(egress) != 0 {
		t.Errorf("[] should clear egress to an empty non-nil list, got %#v", egress)
	}
	write(parent, "[confinement]\nlaunch-unit = false\n")
	if _, egress, _ := resolve(); !reflect.DeepEqual(egress, []string{"a:1"}) {
		t.Errorf("nil should inherit, got %v", egress)
	}
}

// [confinement] enabled (the feature) and default-spawn (the default for a
// spawn) are plain scalars across the trusted layers: the LAST layer that sets
// each wins, so unlike launch-unit a nearer layer can turn them off. launch-unit
// is the deprecated alias for both true; each new key overrides the alias's
// value for itself only; default-spawn defaults to true when unset.
func TestTrustedConfinementPolicy(t *testing.T) {
	const (
		on  = "[confinement]\n"
		yes = true
		no  = false
	)
	cases := []struct {
		name                 string
		global, parent, repo string
		enabled, defaultOn   bool
	}{
		{"nothing set", "", "", "", no, no},
		{"launch-unit alone is both", on + "launch-unit = true\n", "", "", yes, yes},
		{"launch-unit stays sticky-on", on + "launch-unit = true\n", on + "launch-unit = false\n", "", yes, yes},
		{"enabled alone defaults default-spawn on", on + "enabled = true\n", "", "", yes, yes},
		{"enabled with default-spawn off", on + "enabled = true\ndefault-spawn = false\n", "", "", yes, no},
		{"default-spawn alone does not enable", on + "default-spawn = true\n", "", "", no, no},
		{"enabled is not sticky: a nearer false wins", on + "enabled = true\n", on + "enabled = false\n", "", no, no},
		{"default-spawn last layer wins", on + "enabled = true\ndefault-spawn = false\n", on + "default-spawn = true\n", "", yes, yes},
		{"repo layer is not trusted", "", "", on + "enabled = true\n", no, no},
		{"repo layer cannot turn the feature off", on + "enabled = true\n", "", on + "enabled = false\n", yes, yes},
		{"default-spawn overrides the alias for itself only", on + "launch-unit = true\n", on + "default-spawn = false\n", "", yes, no},
		{"enabled=false overrides the alias", on + "launch-unit = true\n", on + "enabled = false\n", "", no, no},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			repo := filepath.Join(home, "work", "repo")
			if err := os.MkdirAll(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			for path, body := range map[string]string{
				filepath.Join(home, ".config", "spinclass", "sweatfile"): tc.global,
				filepath.Join(home, "work", "sweatfile"):                 tc.parent,
				filepath.Join(repo, "sweatfile"):                         tc.repo,
			} {
				if body == "" {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			h, err := LoadHierarchy(home, repo)
			if err != nil {
				t.Fatal(err)
			}
			p, _, _ := TrustedConfinement(h, home, repo)
			if p.Enabled != tc.enabled {
				t.Errorf("Enabled = %v, want %v", p.Enabled, tc.enabled)
			}
			if got := p.DefaultConfines(); got != tc.defaultOn {
				t.Errorf("DefaultConfines() = %v, want %v", got, tc.defaultOn)
			}
		})
	}
}

// [permissions] deny-sweatfile-edits is read only from layers above the repo, a
// scalar override (last trusted layer wins), off when unset.
func TestTrustedDenySweatfileEdits(t *testing.T) {
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
	resolve := func() (bool, []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		return TrustedDenySweatfileEdits(h, home, repo)
	}

	if deny, untrusted := resolve(); deny || len(untrusted) != 0 {
		t.Errorf("unset: got (%v, %v), want (false, none)", deny, untrusted)
	}
	write(repoFile, "[permissions]\ndeny-sweatfile-edits = true\n")
	if deny, untrusted := resolve(); deny || !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("repo layer: got (%v, %v), want (false, [%s])", deny, untrusted, repoFile)
	}
	write(global, "[permissions]\ndeny-sweatfile-edits = true\n")
	if deny, _ := resolve(); !deny {
		t.Error("a global true is not honoured")
	}
	write(parent, "[permissions]\ndeny-sweatfile-edits = false\n")
	if deny, _ := resolve(); deny {
		t.Error("the last trusted layer should win")
	}
}

// [permissions] allow-self-close (#389) follows the same rule as
// deny-sweatfile-edits: read only from layers above the repo, last trusted
// layer wins, off when unset. A repo layer cannot switch it on, so a session
// cannot give itself the right to end itself.
func TestTrustedAllowSelfClose(t *testing.T) {
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
	resolve := func() (bool, []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		return TrustedAllowSelfClose(h, home, repo)
	}

	if allow, untrusted := resolve(); allow || len(untrusted) != 0 {
		t.Errorf("unset: got (%v, %v), want (false, none)", allow, untrusted)
	}
	write(repoFile, "[permissions]\nallow-self-close = true\n")
	if allow, untrusted := resolve(); allow || !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("repo layer: got (%v, %v), want (false, [%s])", allow, untrusted, repoFile)
	}
	write(global, "[permissions]\nallow-self-close = true\n")
	if allow, _ := resolve(); !allow {
		t.Error("a global true is not honoured")
	}
	write(parent, "[permissions]\nallow-self-close = false\n")
	if allow, _ := resolve(); allow {
		t.Error("the last trusted layer should win")
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
