package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// fakeToolsOnPath puts executable files named fibby and piggy on PATH, the
// thing the launcher must never resolve a program from.
func fakeToolsOnPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{"fibby", "piggy"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if p, err := exec.LookPath("fibby"); err != nil || filepath.Dir(p) != dir {
		t.Skipf("the temp dir cannot hold runnable files here (LookPath fibby = %q, %v): this test would prove nothing", p, err)
	}
	return dir
}

// khStore stands a temporary directory in for the Nix store and puts an
// executable fibby and piggy in it.
func khStore(t *testing.T) (root, fibby, piggy string) {
	t.Helper()
	r, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prev := sweatfileio.KeyholderStoreRoot
	sweatfileio.KeyholderStoreRoot = r
	t.Cleanup(func() { sweatfileio.KeyholderStoreRoot = prev })
	mk := func(rel string) string {
		p := filepath.Join(r, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return r, mk("h1-fibby-holder-0.1.0/bin/fibby"), mk("h2-piggy-0.1.23/bin/piggy")
}

// khRealChecks routes keyholderLauncherFn through the real tool checks (the
// production path) but hands back a fake launcher, so nothing is ever started.
// It records the tools the check saw.
func khRealChecks(t *testing.T, f *khFakeRunner) *keyholder.Tools {
	t.Helper()
	// A short directory: the holder's socket paths have a 107-byte limit.
	base, err := os.MkdirTemp("", "khb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	var got keyholder.Tools
	prev := keyholderLauncherFn
	t.Cleanup(func() { keyholderLauncherFn = prev })
	keyholderLauncherFn = func(tools keyholder.Tools) (*keyholder.Launcher, error) {
		if _, err := realKeyholderLauncher(tools); err != nil {
			return nil, err
		}
		got = tools
		return &keyholder.Launcher{Base: filepath.Join(base, "kh"), Tools: tools, Run: f, Wait: time.Second}, nil
	}
	return &got
}

func khToml(extra string) string {
	return "[keyholder]\nmode = \"on\"\n" + extra
}

func khSetting(name, value string) string { return fmt.Sprintf("%s = %q\n", name, value) }

// The pair runs only what the host's configuration names: an unset path refuses
// the launch even when both programs are on PATH, which a session can influence.
func TestKeyholderOnWithoutTheTrustedPathsRefusesEvenWithBothProgramsOnPATH(t *testing.T) {
	fakeToolsOnPath(t)
	khStore(t)
	home, repo := khHome(t, khToml(""))
	f := newKhFake(t)
	got := khRealChecks(t, f)
	env, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{})
	if err == nil || env != nil || !strings.Contains(err.Error(), "[keyholder] fibby is not set") || !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("got (%v, %v), want a refusal naming [keyholder] fibby and PATH", env, err)
	}
	if len(f.started) != 0 || got.Fibby != "" {
		t.Errorf("a unit was started (%v) or tools were accepted (%+v) without the setting", f.started, *got)
	}
}

func TestKeyholderOnNamesTheOtherProgramWhenOnlyOneIsSet(t *testing.T) {
	_, fibby, _ := khStore(t)
	home, repo := khHome(t, khToml(khSetting("fibby", fibby)))
	khRealChecks(t, newKhFake(t))
	if _, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "[keyholder] piggy is not set") {
		t.Fatalf("got %v, want the refusal to name [keyholder] piggy", err)
	}
}

func TestKeyholderOnRefusesARelativeOrOutsideTheStorePath(t *testing.T) {
	root, fibby, piggy := khStore(t)
	outside := filepath.Join(t.TempDir(), "piggy")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "h3-link", "bin", "piggy")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	noexec := filepath.Join(root, "h4-noexec", "bin", "piggy")
	if err := os.MkdirAll(filepath.Dir(noexec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(noexec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ fibby, piggy, want string }{
		"relative":                 {"bin/fibby", piggy, "not an absolute path"},
		"outside the store":        {fibby, outside, "not under " + root},
		"a link leaving the store": {fibby, link, "leaves " + root},
		"not executable":           {fibby, noexec, "not executable"},
	} {
		home, repo := khHome(t, khToml(khSetting("fibby", c.fibby)+khSetting("piggy", c.piggy)))
		f := newKhFake(t)
		khRealChecks(t, f)
		env, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{})
		if err == nil || env != nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got (%v, %v), want a refusal containing %q", name, env, err, c.want)
		}
		if len(f.started) != 0 {
			t.Errorf("%s: a pair was started (%v) on a refused path", name, f.started)
		}
	}
}

// A repo or worktree layer's paths are never honoured: they are ignored and
// reported, and the launch is refused when the trusted layers name none.
func TestKeyholderPathsFromARepoLayerAreIgnoredAndReported(t *testing.T) {
	_, fibby, piggy := khStore(t)
	home, repo := khHome(t, khToml(""))
	if err := os.WriteFile(filepath.Join(repo, "sweatfile"), []byte("[keyholder]\n"+khSetting("fibby", fibby)+khSetting("piggy", piggy)), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newKhFake(t)
	got := khRealChecks(t, f)
	var stderr bytes.Buffer
	env, err := keyholderSessionEnv(home, repo, "repo/a", &stderr)
	if err == nil || env != nil || !strings.Contains(err.Error(), "[keyholder] fibby is not set") {
		t.Fatalf("got (%v, %v): a repo layer's valid paths must not satisfy the setting", env, err)
	}
	if got.Fibby != "" || len(f.started) != 0 {
		t.Errorf("tools %+v accepted or units started %v from a repo layer", *got, f.started)
	}
	if !strings.Contains(stderr.String(), "ignoring [keyholder] in "+filepath.Join(repo, "sweatfile")) {
		t.Errorf("the ignored repo layer was not reported: %q", stderr.String())
	}
}

// Valid trusted paths reach the launcher exactly, and nothing from PATH does.
func TestKeyholderOnHandsTheLauncherExactlyTheTrustedPaths(t *testing.T) {
	fakeToolsOnPath(t)
	_, fibby, piggy := khStore(t)
	home, repo := khHome(t, khToml(khSetting("fibby", fibby)+khSetting("piggy", piggy)))
	f := newKhFake(t)
	got := khRealChecks(t, f)
	env, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{})
	if err != nil || env["SSH_AUTH_SOCK"] == "" {
		t.Fatalf("got (%v, %v), want the holder's environment", env, err)
	}
	if got.Fibby != fibby || got.Piggy != piggy {
		t.Errorf("the launcher got %+v, want exactly %q and %q", *got, fibby, piggy)
	}
}

// With the switch off nothing is read or validated beyond parsing: a path no
// store program could satisfy does not make a session launch fail.
func TestKeyholderOffIgnoresTheToolPathsEntirely(t *testing.T) {
	khStore(t)
	home, repo := khHome(t, "[keyholder]\nmode = \"off\"\nfibby = \"bin/relative\"\npiggy = \"/not/the/store\"\n")
	f := newKhFake(t)
	built := 0
	prev := keyholderLauncherFn
	t.Cleanup(func() { keyholderLauncherFn = prev })
	keyholderLauncherFn = func(keyholder.Tools) (*keyholder.Launcher, error) {
		built++
		return nil, fmt.Errorf("must not be built")
	}
	env, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{})
	if err != nil || env != nil || built != 0 || len(f.started) != 0 {
		t.Errorf("off: got (%v, %v), built=%d started=%v, want nothing at all", env, err, built, f.started)
	}
}

// The hidden debugging command may name its own absolute paths: a person at a
// terminal chose them. Naming only one is refused.
func TestKeyholderPairCommandNamesItsOwnProgramsOnlyWhenGivenBoth(t *testing.T) {
	if _, err := launcherForTools("/opt/fibby", "/opt/piggy"); err != nil {
		t.Fatalf("explicit absolute paths were refused: %v", err)
	}
	if _, err := launcherForTools("/opt/fibby", ""); err == nil {
		t.Error("a launcher was built with only one program named")
	}
	if _, err := launcherForTools("fibby", "/opt/piggy"); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Errorf("a relative name was accepted: %v", err)
	}
}
