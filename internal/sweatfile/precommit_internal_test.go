package sweatfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

func strptr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

func TestDispatcherScript(t *testing.T) {
	s := dispatcherScript("/orig/hooks", "/wt/.spinclass/hooks", "conformist --staged --exit-zero-on-fix", "deadbeefcafe")
	if !strings.Contains(s, "bin='conformist'") {
		t.Errorf("expected baked bin, got:\n%s", s)
	}
	if !strings.Contains(s, "cmd='conformist --staged --exit-zero-on-fix'") {
		t.Errorf("expected baked command, got:\n%s", s)
	}
	if !strings.Contains(s, `command -v "$bin"`) || !strings.Contains(s, `sh -c "$cmd"`) {
		t.Errorf("expected PATH guard + command run, got:\n%s", s)
	}
	// A nonzero formatter exit is surfaced loudly, not silently swallowed.
	if !strings.Contains(s, "staged content NOT repaired") {
		t.Errorf("expected loud nonzero warning, got:\n%s", s)
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

	// spinclass#267: the baked lock hash + the flake.lock-drift re-eval branch.
	if !strings.Contains(s, "lockhash='deadbeefcafe'") {
		t.Errorf("expected baked flake.lock hash, got:\n%s", s)
	}
	if !strings.Contains(s, "git hash-object flake.lock") {
		t.Errorf("expected live flake.lock hash compare, got:\n%s", s)
	}
	if !strings.Contains(s, `nix develop --command sh -c "$cmd"`) {
		t.Errorf("expected the current-devShell re-eval branch, got:\n%s", s)
	}
	if !strings.Contains(s, "flake.lock changed since this session") {
		t.Errorf("expected the re-eval heads-up, got:\n%s", s)
	}

	// A `sh -c '...'` command guards on the sh binary, not the inner tool.
	if got := dispatcherScript("/o", "/s", "sh -c 'foo bar'", ""); !strings.Contains(got, "bin='sh'") {
		t.Errorf("expected guard on sh, got:\n%s", got)
	}
	// With no flake.lock (empty baked hash), the drift branch is inert.
	if got := dispatcherScript("/o", "/s", "conformist", ""); !strings.Contains(got, "lockhash=''") {
		t.Errorf("expected empty baked hash to disable the drift branch, got:\n%s", got)
	}
}

// TestDispatcherScriptRendersValidSh guards the concat→template refactor
// (spinclass#269): the rendered dispatcher must always be syntactically valid
// POSIX sh, checked directly with `sh -n` (independent of the git-driven
// execution tests). Both branches — with and without a baked lock hash — render.
func TestDispatcherScriptRendersValidSh(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lockHash string
	}{
		{"with-lockhash", "deadbeefcafe"},
		{"empty-lockhash", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := dispatcherScript("/orig/hooks", "/wt/.spinclass/hooks",
				"conformist --staged --exit-zero-on-fix", tc.lockHash)
			cmd := exec.Command("sh", "-n")
			cmd.Stdin = strings.NewReader(script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("rendered dispatcher is not valid sh: %v\n%s\n--- script ---\n%s", err, out, script)
			}
		})
	}
}

// TestInstallPreCommitHookBakesFlakeLockHash: when the worktree has a flake.lock,
// its git content hash is baked into the dispatcher as the session-start lock
// identity (spinclass#267). No flake.lock ⇒ empty hash ⇒ drift branch inert
// (covered by the other install tests, whose repos have no flake.lock).
func TestInstallPreCommitHookBakesFlakeLockHash(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")

	if err := os.WriteFile(filepath.Join(wt, "flake.lock"), []byte(`{"nodes":{}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := flakeLockHash(wt)
	if want == "" {
		t.Fatal("expected a non-empty flake.lock hash")
	}

	sf := Sweatfile{Hooks: &Hooks{PreCommit: strptr("conformist --staged")}}
	if err := sf.installPreCommitHook(wt); err != nil {
		t.Fatalf("install: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(wt, ".spinclass", "hooks", "pre-commit"))
	if !strings.Contains(string(body), "lockhash='"+want+"'") {
		t.Errorf("expected baked flake.lock hash %q in dispatcher:\n%s", want, body)
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

// A formatter that fails its own arg validation (mimics a stale conformist
// rejecting --exit-zero-on-fix, spinclass#183 review) must not silently no-op:
// the commit still proceeds (non-blocking) but the failure is surfaced loudly
// with the formatter's stderr.
func TestInstallPreCommitHookSurfacesFormatterError(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")

	binDir := t.TempDir()
	writeExec(t, filepath.Join(binDir, "badfmt"),
		"#!/bin/sh\necho 'badfmt: --exit-zero-on-fix requires --commit' >&2\nexit 2\n")

	sf := Sweatfile{Hooks: &Hooks{PreCommit: strptr("badfmt --staged --exit-zero-on-fix")}}
	if err := sf.installPreCommitHook(wt); err != nil {
		t.Fatalf("install: %v", err)
	}

	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitPath(t, wt, binDir, "add", "f.txt")

	cmd := exec.Command("git", "-C", wt, "commit", "-m", "c")
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("commit must not be blocked by a non-blocking formatter: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "staged content NOT repaired") {
		t.Errorf("expected loud formatter-error banner in commit output, got:\n%s", got)
	}
	if !strings.Contains(got, "requires --commit") {
		t.Errorf("expected the formatter's own stderr passed through, got:\n%s", got)
	}
}

// TestInstallPreCommitHookReEvalsOnFlakeLockDrift proves the spinclass#267
// control flow: when flake.lock has drifted since install (live git-hash != baked
// hash), the dispatcher routes the formatter through `nix develop --command`
// instead of the frozen PATH. Stub `nix` and formatter binaries record that they
// fired — no real devShell build.
func TestInstallPreCommitHookReEvalsOnFlakeLockDrift(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")

	// flake.lock present at install ⇒ its content hash is baked in.
	lockPath := filepath.Join(wt, "flake.lock")
	if err := os.WriteFile(lockPath, []byte(`{"v":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	nixMarker := filepath.Join(t.TempDir(), "nix-called")
	fmtMarker := filepath.Join(t.TempDir(), "fmt-ran")
	// Stub `nix`: on `develop --command <rest...>`, record it fired then exec the
	// rest (the `sh -c "$cmd"` the dispatcher hands it).
	writeExec(t, filepath.Join(binDir, "nix"),
		"#!/bin/sh\necho called > "+shSingleQuote(nixMarker)+"\nshift 2\nexec \"$@\"\n")
	writeExec(t, filepath.Join(binDir, "fakefmt"),
		"#!/bin/sh\necho ran > "+shSingleQuote(fmtMarker)+"\nexit 0\n")

	sf := Sweatfile{Hooks: &Hooks{PreCommit: strptr("fakefmt")}}
	if err := sf.installPreCommitHook(wt); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Drift the lock so the live hash differs from the baked one.
	if err := os.WriteFile(lockPath, []byte(`{"v":2}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitPath(t, wt, binDir, "add", "f.txt")
	runGitPath(t, wt, binDir, "commit", "-q", "-m", "c")

	if _, err := os.Stat(nixMarker); err != nil {
		t.Errorf("flake.lock drift should re-eval via `nix develop`, but stub nix was not called: %v", err)
	}
	if _, err := os.Stat(fmtMarker); err != nil {
		t.Errorf("the formatter should still run (under the re-eval): %v", err)
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
