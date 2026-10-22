package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gitpkg "code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/store"
)

func TestConfinedHardeningShape(t *testing.T) {
	s := store.Session{
		Dir:         "/r/.worktrees/feat",
		Confinement: store.ConfinementConfined,
		Repo: store.Repo{
			CommonDir:      "/r/.git",
			GitDir:         "/r/.git/worktrees/feat",
			CredentialFile: "/r/.worktrees/feat/.spinclass/git-credentials",
		},
	}
	h, ok := confinedHardening(s)
	if !ok {
		t.Fatal("a confined session must be hardened")
	}
	for _, want := range []string{
		"GIT_DIR=/r/.git/worktrees/feat", "GIT_COMMON_DIR=/r/.git", "GIT_WORK_TREE=/r/.worktrees/feat",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_ATTR_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0",
		"GIT_CEILING_DIRECTORIES=/r/.worktrees",
	} {
		if !slices.Contains(h.Env, want) {
			t.Errorf("env lacks %s: %v", want, h.Env)
		}
	}
	if slices.ContainsFunc(h.Env, func(e string) bool { return strings.HasPrefix(e, "GIT_CONFIG_GLOBAL=") }) {
		t.Error("the frontend's global config carries the commit identity and must be kept")
	}
	joined := strings.Join(h.Args, " ")
	for _, want := range []string{
		"core.fsmonitor=false", "core.sshCommand=", "core.pager=cat", "core.editor=true",
		"diff.external=", "protocol.ext.allow=never", "core.attributesFile=/dev/null",
		"credential.helper= -c credential.helper=!f() { test \"$1\" = get && exec git credential-store --file='/r/.worktrees/feat/.spinclass/git-credentials' get; }; f",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args lack %s: %s", want, joined)
		}
	}
	// the reset must come before the pinned helper
	if strings.Index(joined, "credential.helper= ") > strings.Index(joined, "credential.helper=!") {
		t.Errorf("credential.helper reset must precede the pinned helper: %s", joined)
	}
}

func TestConfinedHardeningWithoutRepoLeavesDiscoveryAlone(t *testing.T) {
	h, ok := confinedHardening(store.Session{Dir: "/w", Confinement: store.ConfinementUnresolved})
	if !ok {
		t.Fatal("an unresolved caller counts as confined")
	}
	if slices.ContainsFunc(h.Env, func(e string) bool { return strings.HasPrefix(e, "GIT_DIR=") }) {
		t.Errorf("no trusted repo, yet GIT_DIR set: %v", h.Env)
	}
	if strings.Contains(strings.Join(h.Args, " "), "credential-store") {
		t.Error("no credential minted, yet a store helper is pinned")
	}
}

func TestUnconfinedIsNotHardened(t *testing.T) {
	if _, ok := confinedHardening(store.Session{Dir: "/w", Confinement: store.ConfinementUnconfined}); ok {
		t.Error("an unconfined caller must run git exactly as before")
	}
	if got := withoutExternalDiffForConfined(store.Session{Confinement: store.ConfinementUnconfined}, []string{"diff", "--stat"}); !slices.Equal(got, []string{"diff", "--stat"}) {
		t.Errorf("unconfined diff argv changed: %v", got)
	}
}

func TestConfineScopesToTheSessionWorktree(t *testing.T) {
	dir := t.TempDir()
	wt := filepath.Join(dir, "wt")
	other := filepath.Join(dir, "other")
	s := store.Session{
		Dir: wt, Confinement: store.ConfinementConfined,
		Repo: store.Repo{CommonDir: "/c", GitDir: "/c/worktrees/wt"},
	}
	if err := os.MkdirAll(filepath.Join(wt, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	release := confineGit(s, filepath.Join(wt, "sub"))
	if h, ok := gitpkg.HardeningFor(filepath.Join(wt, "sub")); !ok || !slices.Contains(h.Env, "GIT_DIR=/c/worktrees/wt") {
		t.Errorf("a subdirectory of the worktree must carry the explicit repo: %v %v", h, ok)
	}
	if _, ok := gitpkg.HardeningFor(other); ok {
		t.Error("a sibling directory must not be hardened by the session's registration")
	}
	release()
	if _, ok := gitpkg.HardeningFor(wt); ok {
		t.Error("release must unregister")
	}

	// A repo_path outside the worktree gets the neutralisers, never the session repo.
	release = confineGit(s, other)
	defer release()
	h, ok := gitpkg.HardeningFor(other)
	if !ok || slices.ContainsFunc(h.Env, func(e string) bool { return strings.HasPrefix(e, "GIT_DIR=") }) {
		t.Errorf("outside dir: ok=%v env=%v, want neutralisers without GIT_DIR", ok, h.Env)
	}
}

// markerRepo is a repo with a linked worktree "feat" whose config.worktree sets
// core.fsmonitor and diff.external to scripts that append to a marker file.
type markerRepo struct {
	common, wt, fsmarker, diffmarker string
	session                          store.Session
}

func newMarkerRepo(t *testing.T) markerRepo {
	t.Helper()
	repo := newRepo(t)
	common := filepath.Join(repo, ".git")
	wt := filepath.Join(repo, ".worktrees", "feat")
	mustRun(t, repo, "worktree", "add", "-b", "feat", wt)
	mustRun(t, repo, "config", "extensions.worktreeConfig", "true")

	scripts := t.TempDir()
	m := markerRepo{common: common, wt: wt, fsmarker: filepath.Join(scripts, "fsmonitor.ran"), diffmarker: filepath.Join(scripts, "diff.ran")}
	fsScript := filepath.Join(scripts, "fsmonitor.sh")
	diffScript := filepath.Join(scripts, "diff.sh")
	writeExec(t, fsScript, "#!/bin/sh\necho ran >> "+m.fsmarker+"\nexit 1\n")
	writeExec(t, diffScript, "#!/bin/sh\necho ran >> "+m.diffmarker+"\nexit 0\n")
	cfg := "[core]\n\tfsmonitor = " + fsScript + "\n[diff]\n\texternal = " + diffScript + "\n"
	if err := os.WriteFile(filepath.Join(common, "worktrees", "feat", "config.worktree"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, wt, "a.txt", "one\ntwo\n")
	m.session = store.Session{
		Dir: wt, Branch: "feat", Confinement: store.ConfinementConfined,
		Repo: store.Repo{CommonDir: common, GitDir: filepath.Join(common, "worktrees", "feat")},
	}
	return m
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func ran(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// The unconfined call runs the config-defined fsmonitor and diff driver, which
// proves the marker tests below can fail; the confined call must not run them.
func TestConfinedCallDoesNotRunWorktreeConfigCommands(t *testing.T) {
	t.Run("control_unconfined_runs_them", func(t *testing.T) {
		m := newMarkerRepo(t)
		s := m.session
		s.Confinement = store.ConfinementUnconfined
		if _, err := doAs(t, s, "stage", "status", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := doAs(t, s, "stage", "diff", nil); err != nil {
			t.Fatal(err)
		}
		if !ran(m.fsmarker) {
			t.Error("control: core.fsmonitor in config.worktree did not run for an unconfined status; the confined assertion below proves nothing")
		}
		if !ran(m.diffmarker) {
			t.Error("control: diff.external in config.worktree did not run for an unconfined diff")
		}
	})
	t.Run("confined does not", func(t *testing.T) {
		m := newMarkerRepo(t)
		out, err := doAs(t, m.session, "stage", "status", nil)
		if err != nil || !strings.Contains(out, "## feat") || !strings.Contains(out, "M a.txt") {
			t.Fatalf("confined status: %q, %v", out, err)
		}
		out, err = doAs(t, m.session, "stage", "diff", nil)
		if err != nil || !strings.Contains(out, "+two") {
			t.Fatalf("confined diff: %q, %v", out, err)
		}
		if ran(m.fsmarker) {
			t.Error("a confined call ran core.fsmonitor from config.worktree")
		}
		if ran(m.diffmarker) {
			t.Error("a confined call ran diff.external from config.worktree")
		}
	})
}

// A .git pointer file the unit repointed at another admin dir (whose
// config.worktree carries a marker-writing fsmonitor) is not followed by a
// confined call, which names its gitdir itself, but is by an unconfined one.
func TestConfinedCallIgnoresARepointedGitFile(t *testing.T) {
	setup := func(t *testing.T) (markerRepo, string) {
		m := newMarkerRepo(t)
		mustRun(t, m.wt, "worktree", "add", "-b", "evil", filepath.Join(filepath.Dir(m.wt), "evil"))
		// The evil admin dir's config runs the marker script; feat's own does not.
		evilCfg := filepath.Join(m.common, "worktrees", "evil", "config.worktree")
		cfg, err := os.ReadFile(filepath.Join(m.common, "worktrees", "feat", "config.worktree"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evilCfg, cfg, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(m.common, "worktrees", "feat", "config.worktree"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		pointer := filepath.Join(m.wt, ".git")
		if err := os.WriteFile(pointer, []byte("gitdir: "+filepath.Join(m.common, "worktrees", "evil")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Setup's own git runs may have fired the scripts; only the calls under test count.
		_ = os.Remove(m.fsmarker)
		_ = os.Remove(m.diffmarker)
		return m, pointer
	}
	t.Run("control_unconfined_follows_it", func(t *testing.T) {
		m, _ := setup(t)
		s := m.session
		s.Confinement = store.ConfinementUnconfined
		out, err := doAs(t, s, "stage", "status", nil)
		if !ran(m.fsmarker) {
			t.Errorf("control: the repointed .git file did not lead an unconfined status to the evil config (out %q, err %v)", out, err)
		}
	})
	t.Run("confined does not", func(t *testing.T) {
		m, _ := setup(t)
		out, err := doAs(t, m.session, "stage", "status", nil)
		if err != nil || !strings.Contains(out, "## feat") {
			t.Fatalf("confined status: %q, %v", out, err)
		}
		if ran(m.fsmarker) {
			t.Error("a confined call followed the repointed .git file")
		}
	})
}
