package sweatfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/testgit"
)

func strptr(s string) *string { return &s }

func TestPreCommitScriptBinGuard(t *testing.T) {
	s := preCommitScript("conformist --staged --exit-zero-on-fix")
	if !strings.Contains(s, "command -v 'conformist'") {
		t.Errorf("expected PATH guard on conformist, got:\n%s", s)
	}
	if !strings.Contains(s, "sh -c 'conformist --staged --exit-zero-on-fix'") {
		t.Errorf("expected command body, got:\n%s", s)
	}
	if !strings.HasSuffix(s, "exit 0\n") {
		t.Errorf("expected non-blocking exit 0 tail, got:\n%s", s)
	}
	// A `sh -c '...'` command guards on the sh binary, not the inner tool.
	if got := preCommitScript("sh -c 'foo bar'"); !strings.Contains(got, "command -v 'sh'") {
		t.Errorf("expected guard on sh, got:\n%s", got)
	}
}

func TestShSingleQuote(t *testing.T) {
	if got := shSingleQuote("a"); got != "'a'" {
		t.Errorf("shSingleQuote(a) = %q", got)
	}
	if got := shSingleQuote("a'b"); got != `'a'\''b'` {
		t.Errorf("shSingleQuote(a'b) = %q", got)
	}
}

func TestInstallPreCommitHook(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")

	sf := Sweatfile{Hooks: &Hooks{PreCommit: strptr("conformist --staged --exit-zero-on-fix")}}
	if err := sf.installPreCommitHook(wt); err != nil {
		t.Fatalf("install: %v", err)
	}

	hookPath := filepath.Join(wt, ".spinclass", "hooks", "pre-commit")
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("hook not written: %v", err)
	}
	if info.Mode()&0o100 == 0 {
		t.Errorf("hook not executable: %v", info.Mode())
	}
	body, _ := os.ReadFile(hookPath)
	if !strings.Contains(string(body), "conformist --staged --exit-zero-on-fix") {
		t.Errorf("hook missing command:\n%s", body)
	}

	// Worktree-scoped: the worktree resolves core.hooksPath, the main checkout
	// does NOT (the load-bearing isolation, verified by the design spike).
	hp, err := git.Run(wt, "config", "--get", "core.hooksPath")
	if err != nil || hp == "" {
		t.Fatalf("worktree core.hooksPath unset: %q %v", hp, err)
	}
	if mainHP, _ := git.Run(repo, "config", "--get", "core.hooksPath"); mainHP != "" {
		t.Errorf("main checkout leaked core.hooksPath: %q", mainHP)
	}

	// Idempotent: a second install (mirrors resume re-running Apply) is clean.
	if err := sf.installPreCommitHook(wt); err != nil {
		t.Fatalf("second install: %v", err)
	}
}

func TestInstallPreCommitHookFiresOnCommit(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")

	// A stand-in formatter on PATH that drops a marker, proving the hook fired
	// at commit time. The dir is prepended to PATH for the commit invocation.
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	writeExec(t, filepath.Join(binDir, "fakefmt"),
		"#!/bin/sh\necho fired > "+shSingleQuote(marker)+"\nexit 0\n")

	sf := Sweatfile{Hooks: &Hooks{PreCommit: strptr("fakefmt")}}
	if err := sf.installPreCommitHook(wt); err != nil {
		t.Fatalf("install: %v", err)
	}

	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitPath(t, wt, binDir, "add", "f.txt")
	runGitPath(t, wt, binDir, "commit", "-q", "-m", "add f")

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("pre-commit hook did not fire at commit time: %v", err)
	}
}

func TestInstallPreCommitHookInactiveIsNoop(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")

	sf := Sweatfile{Hooks: &Hooks{}}
	if err := sf.installPreCommitHook(wt); err != nil {
		t.Fatalf("inactive install should be a no-op: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".spinclass", "hooks", "pre-commit")); !os.IsNotExist(err) {
		t.Errorf("inactive install wrote a hook")
	}
	if v, _ := git.Run(wt, "config", "--get", "extensions.worktreeConfig"); v != "" {
		t.Errorf("inactive install enabled extensions.worktreeConfig: %q", v)
	}
}

func TestInstallPreCommitHookRefusesCommonCoreWorktree(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")

	// Plant the footgun: core.worktree in the common config. The installer must
	// refuse to enable extensions.worktreeConfig rather than risk the checkout.
	if _, err := git.Run(repo, "config", "core.worktree", repo); err != nil {
		t.Fatalf("set core.worktree: %v", err)
	}

	sf := Sweatfile{Hooks: &Hooks{PreCommit: strptr("conformist --staged")}}
	err := sf.installPreCommitHook(wt)
	if err == nil {
		t.Fatal("expected refusal when core.worktree is set in the common config")
	}
	if !strings.Contains(err.Error(), "core.worktree") {
		t.Errorf("unexpected error: %v", err)
	}
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// runGitPath runs a git command in dir with extraPATH prepended to PATH, so a
// stand-in tool placed there is found by an installed hook.
func runGitPath(t *testing.T, dir, extraPATH string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+extraPATH+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
