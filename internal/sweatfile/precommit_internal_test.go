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
func boolPtr(b bool) *bool    { return &b }

func TestDispatcherScript(t *testing.T) {
	s := dispatcherScript("/orig/hooks", "/wt/.spinclass/hooks", "conformist --staged --exit-zero-on-fix")
	if !strings.Contains(s, "command -v 'conformist'") {
		t.Errorf("expected PATH guard on conformist, got:\n%s", s)
	}
	if !strings.Contains(s, "sh -c 'conformist --staged --exit-zero-on-fix'") {
		t.Errorf("expected command body, got:\n%s", s)
	}
	// Delegates to the original same-named hook, preserving exit code via exec.
	if !strings.Contains(s, `exec "$orig/$hook" "$@"`) {
		t.Errorf("expected delegation exec, got:\n%s", s)
	}
	if !strings.Contains(s, "orig='/orig/hooks'") || !strings.Contains(s, "self='/wt/.spinclass/hooks'") {
		t.Errorf("expected baked orig/self paths, got:\n%s", s)
	}
	if !strings.HasSuffix(s, "exit 0\n") {
		t.Errorf("expected non-blocking exit 0 tail, got:\n%s", s)
	}
	// A `sh -c '...'` command guards on the sh binary, not the inner tool.
	if got := dispatcherScript("/o", "/s", "sh -c 'foo bar'"); !strings.Contains(got, "command -v 'sh'") {
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

	// pre-commit is a symlink to the dispatcher; Stat/ReadFile follow it.
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

func TestInstallPreCommitHookComposesNativeHooks(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")

	// Native hooks in the shared common hooks dir ($GIT_COMMON_DIR/hooks):
	// a pre-commit (to compose with ours) and a post-commit (a non-pre-commit
	// type that must still fire under our overriding core.hooksPath).
	nativeDir := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(t.TempDir(), "events.log")
	writeExec(t, filepath.Join(nativeDir, "pre-commit"),
		"#!/bin/sh\necho native-pre >> "+shSingleQuote(events)+"\nexit 0\n")
	writeExec(t, filepath.Join(nativeDir, "post-commit"),
		"#!/bin/sh\necho native-post >> "+shSingleQuote(events)+"\nexit 0\n")

	binDir := t.TempDir()
	writeExec(t, filepath.Join(binDir, "fmtstub"),
		"#!/bin/sh\necho fmt >> "+shSingleQuote(events)+"\nexit 0\n")

	sf := Sweatfile{Hooks: &Hooks{PreCommit: strptr("fmtstub")}}
	if err := sf.installPreCommitHook(wt); err != nil {
		t.Fatalf("install: %v", err)
	}

	// A non-pre-commit native hook was shimmed so it keeps firing.
	if _, err := os.Lstat(filepath.Join(wt, ".spinclass", "hooks", "post-commit")); err != nil {
		t.Errorf("expected post-commit shim: %v", err)
	}

	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitPath(t, wt, binDir, "add", "f.txt")
	runGitPath(t, wt, binDir, "commit", "-q", "-m", "c")

	data, err := os.ReadFile(events)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	got := string(data)
	for _, want := range []string{"fmt", "native-pre", "native-post"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in composed events:\n%s", want, got)
		}
	}
	// conformist runs before the repo's own pre-commit.
	if strings.Index(got, "fmt") > strings.Index(got, "native-pre") {
		t.Errorf("expected fmt before native-pre, got:\n%s", got)
	}
}

func TestInstallPreCommitHookDisableRestoresNative(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")
	hooksDir := filepath.Join(wt, ".spinclass", "hooks")

	active := Sweatfile{Hooks: &Hooks{PreCommit: strptr("conformist --staged")}}
	if err := active.installPreCommitHook(wt); err != nil {
		t.Fatalf("install: %v", err)
	}
	if hp, _ := git.Run(wt, "config", "--get", "core.hooksPath"); hp == "" {
		t.Fatal("expected core.hooksPath set after install")
	}

	// Disable → restore: our override unset, shims and sentinel removed.
	inactive := Sweatfile{Hooks: &Hooks{PreCommit: strptr("conformist --staged"), DisablePreCommit: boolPtr(true)}}
	if err := inactive.installPreCommitHook(wt); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if hp, _ := git.Run(wt, "config", "--worktree", "--get", "core.hooksPath"); strings.TrimSpace(hp) != "" {
		t.Errorf("expected worktree core.hooksPath unset after disable, got %q", hp)
	}
	if _, err := os.Stat(hooksDir); !os.IsNotExist(err) {
		t.Errorf("expected hooks dir removed after disable")
	}
	if _, err := os.Stat(filepath.Join(wt, ".spinclass", originalSentinelName)); !os.IsNotExist(err) {
		t.Errorf("expected sentinel removed after disable")
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
