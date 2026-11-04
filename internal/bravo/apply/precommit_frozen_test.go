package apply

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// The frozen per-commit hook (FDR 0019, spinclass#459): the formatter is
// resolved from the landed default branch's devShell when the hook is
// installed, baked as an absolute path, and never re-resolved at commit time.

// stubResolver replaces the devShell evaluation for the test's duration.
func stubResolver(t *testing.T, fn func(ctx context.Context, repoPath, sha, bin string) (string, error)) {
	t.Helper()
	prev := resolveFormatterPath
	resolveFormatterPath = fn
	t.Cleanup(func() { resolveFormatterPath = prev })
}

// refusingResolver fails the test if the evaluation is ever reached.
func refusingResolver(t *testing.T) {
	t.Helper()
	stubResolver(t, func(context.Context, string, string, string) (string, error) {
		t.Fatal("the devShell must not be evaluated in this case")
		return "", nil
	})
}

// repoWithFlakeOnMain is a repo whose main commit carries flake.nix and the
// given flake.lock, and a worktree feat cut from it.
func repoWithFlakeOnMain(t *testing.T, lock string) (repo, wt, mainSha string) {
	t.Helper()
	testgit.RequireGit(t)
	repo = t.TempDir()
	testgit.MustInit(t, repo)
	testgit.MustWriteFile(t, repo, "flake.nix", "{ outputs = _: { }; }\n")
	testgit.MustWriteFile(t, repo, "flake.lock", lock)
	testgit.MustGit(t, repo, "add", "flake.nix", "flake.lock")
	testgit.MustGit(t, repo, "commit", "-q", "-m", "flake")
	mainSha = testgit.MustGit(t, repo, "rev-parse", "refs/heads/main")
	wt = filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")
	return repo, wt, mainSha
}

const emptyLock = `{"nodes":{"root":{}},"root":"root","version":7}` + "\n"

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatal(err)
	}
	return ra == rb
}

func TestInstallPreCommitHookFreezesTheFormatterFromTheLandedBase(t *testing.T) {
	repo, wt, mainSha := repoWithFlakeOnMain(t, emptyLock)

	// The formatter lives in a directory that is NOT on PATH at commit time:
	// only the frozen absolute path can reach it.
	frozenDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	frozenFmt := filepath.Join(frozenDir, "fakefmt")
	writeExec(t, frozenFmt, "#!/bin/sh\necho \"$@\" > "+shSingleQuote(marker)+"\nexit 0\n")

	var gotRepo, gotSha, gotBin string
	stubResolver(t, func(_ context.Context, repoPath, sha, bin string) (string, error) {
		gotRepo, gotSha, gotBin = repoPath, sha, bin
		return frozenFmt, nil
	})

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreCommit: strptr("fakefmt --staged")}}
	rec, err := installPreCommitHook(sf, wt)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	// The evaluation is of the main checkout's history at the landed base,
	// for the command's leading word.
	if !sameDir(t, gotRepo, repo) {
		t.Errorf("resolver got repo %q, want %q", gotRepo, repo)
	}
	if gotSha != mainSha || gotBin != "fakefmt" {
		t.Errorf("resolver got (%q, %q), want (%q, fakefmt)", gotSha, gotBin, mainSha)
	}
	if rec == nil || !rec.Frozen || rec.Bin != frozenFmt || rec.BaseSha != mainSha || rec.BaseLabel != "main" || rec.Command != "fakefmt --staged" {
		t.Fatalf("record = %+v, want frozen to %s from main at %s", rec, frozenFmt, mainSha)
	}

	body, _ := os.ReadFile(filepath.Join(wt, ".spinclass", "hooks", "pre-commit"))
	if !strings.Contains(string(body), "bin="+shSingleQuote(frozenFmt)) || !strings.Contains(string(body), "cmd="+shSingleQuote(frozenFmt+" --staged")) {
		t.Errorf("dispatcher does not carry the frozen path:\n%s", body)
	}

	// The sidecar the frontend copies into the session record.
	got, err := ReadHookRecord(wt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bin != frozenFmt || got.BaseSha != mainSha || !got.Frozen {
		t.Errorf("sidecar = %+v", got)
	}

	// A commit with a PATH that cannot find fakefmt still runs the frozen one.
	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitPath(t, wt, t.TempDir(), "add", "f.txt")
	runGitPath(t, wt, t.TempDir(), "commit", "-q", "-m", "c")
	fired, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the frozen formatter did not fire: %v", err)
	}
	if strings.TrimSpace(string(fired)) != "--staged" {
		t.Errorf("the frozen formatter got args %q, want --staged", fired)
	}
}

func TestInstallPreCommitHookFallsBackToTheBareNameWhenResolutionFails(t *testing.T) {
	_, wt, mainSha := repoWithFlakeOnMain(t, emptyLock)
	stubResolver(t, func(context.Context, string, string, string) (string, error) {
		return "", errors.New("evaluation exploded")
	})

	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fired")
	writeExec(t, filepath.Join(binDir, "fakefmt"), "#!/bin/sh\necho fired > "+shSingleQuote(marker)+"\nexit 0\n")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreCommit: strptr("fakefmt")}}
	rec, err := installPreCommitHook(sf, wt)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rec == nil || rec.Frozen || rec.Bin != "fakefmt" || rec.BaseSha != mainSha {
		t.Fatalf("record = %+v, want unfrozen on the bare name with the base recorded", rec)
	}
	if !strings.Contains(rec.Reason, "evaluation exploded") || !strings.Contains(rec.Reason, "main") {
		t.Errorf("reason = %q, want the evaluation error and the base", rec.Reason)
	}

	body, _ := os.ReadFile(filepath.Join(wt, ".spinclass", "hooks", "pre-commit"))
	if !strings.Contains(string(body), "bin='fakefmt'") {
		t.Errorf("dispatcher should carry the bare name:\n%s", body)
	}

	// Today's behaviour: the committing shell's PATH provides the formatter.
	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitPath(t, wt, binDir, "add", "f.txt")
	runGitPath(t, wt, binDir, "commit", "-q", "-m", "c")
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the PATH formatter did not fire: %v", err)
	}
}

func TestInstallPreCommitHookDoesNotEvaluateABaseWithoutAFlake(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")
	refusingResolver(t)

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreCommit: strptr("conformist-pre-commit")}}
	rec, err := installPreCommitHook(sf, wt)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rec == nil || rec.Frozen || !strings.Contains(rec.Reason, "no flake.nix") {
		t.Errorf("record = %+v, want unfrozen for want of a flake.nix at the base", rec)
	}
}

func TestInstallPreCommitHookDoesNotEvaluateABaseWhoseLockReachesTheLocalFilesystem(t *testing.T) {
	lock := `{"nodes":{"leak":{"locked":{"type":"path","path":"/somewhere","narHash":"sha256-x"},"original":{"type":"path","path":"/somewhere"}},"root":{"inputs":{"leak":"leak"}}},"root":"root","version":7}` + "\n"
	_, wt, _ := repoWithFlakeOnMain(t, lock)
	refusingResolver(t)

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreCommit: strptr("conformist-pre-commit")}}
	rec, err := installPreCommitHook(sf, wt)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rec == nil || rec.Frozen || !strings.Contains(rec.Reason, "names input leak on the local filesystem") {
		t.Errorf("record = %+v, want a refusal naming the local input", rec)
	}
}

func TestInstallPreCommitHookRequiresALockBesideTheBaseFlake(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	testgit.MustWriteFile(t, repo, "flake.nix", "{ outputs = _: { }; }\n")
	testgit.MustGit(t, repo, "add", "flake.nix")
	testgit.MustGit(t, repo, "commit", "-q", "-m", "flake without lock")
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")
	refusingResolver(t)

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreCommit: strptr("conformist-pre-commit")}}
	rec, err := installPreCommitHook(sf, wt)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rec == nil || rec.Frozen || !strings.Contains(rec.Reason, "no flake.lock") {
		t.Errorf("record = %+v, want unfrozen for want of a lock", rec)
	}
}

func TestInstallPreCommitHookRejectsARelativeResolution(t *testing.T) {
	_, wt, _ := repoWithFlakeOnMain(t, emptyLock)
	stubResolver(t, func(context.Context, string, string, string) (string, error) { return "bin/fakefmt", nil })

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreCommit: strptr("fakefmt")}}
	rec, err := installPreCommitHook(sf, wt)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rec == nil || rec.Frozen || rec.Bin != "fakefmt" || !strings.Contains(rec.Reason, "not an absolute path") {
		t.Errorf("record = %+v, want the bare name kept", rec)
	}
}

func TestInstallPreCommitHookKeepsAnAbsoluteCommandAsIs(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")
	refusingResolver(t)

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreCommit: strptr("/opt/fmt/bin/fmt --staged")}}
	rec, err := installPreCommitHook(sf, wt)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rec == nil || !rec.Frozen || rec.Bin != "/opt/fmt/bin/fmt" {
		t.Errorf("record = %+v, want an absolute command accepted without evaluation", rec)
	}
}

func TestInstallPreCommitHookDisableRemovesTheRecord(t *testing.T) {
	_, wt, _ := repoWithFlakeOnMain(t, emptyLock)
	stubResolver(t, func(context.Context, string, string, string) (string, error) { return "/nix/store/x/bin/fakefmt", nil })

	active := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreCommit: strptr("fakefmt")}}
	if _, err := installPreCommitHook(active, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHookRecord(wt); err != nil {
		t.Fatalf("expected a record after install: %v", err)
	}
	inactive := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreCommit: strptr("fakefmt"), DisablePreCommit: boolPtr(true)}}
	rec, err := installPreCommitHook(inactive, wt)
	if err != nil || rec != nil {
		t.Fatalf("disable: rec=%+v err=%v", rec, err)
	}
	if _, err := ReadHookRecord(wt); !os.IsNotExist(err) {
		t.Errorf("expected the record removed on disable, got %v", err)
	}
}

func TestFrozenHookStale(t *testing.T) {
	repo, _, mainSha := repoWithFlakeOnMain(t, emptyLock)

	if stale, reason := FrozenHookStale(nil, repo); !stale || !strings.Contains(reason, "no frozen pre-commit hook recorded") {
		t.Errorf("nil record: stale=%v reason=%q", stale, reason)
	}
	unfrozen := unfrozenRec("fakefmt", "nix is not on PATH")
	if stale, reason := FrozenHookStale(&unfrozen, repo); !stale || !strings.Contains(reason, "was not frozen (nix is not on PATH)") {
		t.Errorf("unfrozen record: stale=%v reason=%q", stale, reason)
	}
	frozen := frozenRec("fakefmt", "/nix/store/x/bin/fakefmt", "main", mainSha)
	if stale, reason := FrozenHookStale(&frozen, repo); stale {
		t.Errorf("record at the current base must not be stale: %q", reason)
	}

	// The base moves: a new landing on main.
	testgit.MustWriteFile(t, repo, "n.txt", "n\n")
	testgit.MustGit(t, repo, "add", "n.txt")
	testgit.MustGit(t, repo, "commit", "-q", "-m", "landing")
	newSha := testgit.MustGit(t, repo, "rev-parse", "refs/heads/main")
	stale, reason := FrozenHookStale(&frozen, repo)
	if !stale {
		t.Fatal("record frozen at the old base must be stale")
	}
	for _, want := range []string{"frozen from main at " + mainSha[:7], "main now at " + newSha[:7]} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q lacks %q", reason, want)
		}
	}
}

func TestReinstallPreCommitHookRefreezesFromTheMovedBase(t *testing.T) {
	repo, wt, mainSha := repoWithFlakeOnMain(t, emptyLock)
	var evaluatedAt []string
	stubResolver(t, func(_ context.Context, _, sha, _ string) (string, error) {
		evaluatedAt = append(evaluatedAt, sha)
		return "/nix/store/" + sha[:7] + "/bin/fakefmt", nil
	})
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreCommit: strptr("fakefmt")}}
	if _, err := installPreCommitHook(sf, wt); err != nil {
		t.Fatal(err)
	}

	testgit.MustWriteFile(t, repo, "n.txt", "n\n")
	testgit.MustGit(t, repo, "add", "n.txt")
	testgit.MustGit(t, repo, "commit", "-q", "-m", "landing")
	newSha := testgit.MustGit(t, repo, "rev-parse", "refs/heads/main")

	rec, err := ReinstallPreCommitHook(sf, wt)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.BaseSha != newSha || rec.Bin != "/nix/store/"+newSha[:7]+"/bin/fakefmt" {
		t.Errorf("reinstall record = %+v, want the new base %s", rec, newSha)
	}
	if len(evaluatedAt) != 2 || evaluatedAt[0] != mainSha || evaluatedAt[1] != newSha {
		t.Errorf("evaluations = %v, want [%s %s]", evaluatedAt, mainSha, newSha)
	}
	body, _ := os.ReadFile(filepath.Join(wt, ".spinclass", "hooks", "pre-commit"))
	if !strings.Contains(string(body), newSha[:7]) {
		t.Errorf("dispatcher not rewritten for the new base:\n%s", body)
	}
}

func TestNixDevelopArgvEvaluatesTheLandedCommitWithoutTouchingTheLock(t *testing.T) {
	argv := nixDevelopArgv("/nix/store/n/bin/nix", "/repos/x", "0123456789abcdef0123456789abcdef01234567", "conformist-pre-commit")
	want := []string{
		"/nix/store/n/bin/nix", "develop", "git+file:///repos/x?rev=0123456789abcdef0123456789abcdef01234567",
		"--no-update-lock-file", "--no-write-lock-file",
		"--command", "sh", "-c", `command -v -- "$1"`, "sh", "conformist-pre-commit",
	}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %q, want %q", argv, want)
	}
}

func TestLockNamesLocalInput(t *testing.T) {
	cases := map[string]string{
		emptyLock: "",
		`{"nodes":{"a":{"locked":{"type":"github","owner":"o","repo":"r"},"original":{"type":"github","owner":"o","repo":"r"}},"root":{"inputs":{"a":"a"}}},"root":"root"}`:                      "",
		`{"nodes":{"a":{"locked":{"type":"tarball","url":"https://x/a.tar.gz"},"original":{"type":"tarball","url":"https://x/a.tar.gz"}},"root":{"inputs":{"a":"a"}}},"root":"root"}`:            "",
		`{"nodes":{"leak":{"locked":{"type":"path","path":"/p"},"original":{"type":"path","path":"/p"}},"root":{"inputs":{"leak":"leak"}}},"root":"root"}`:                                       "names input leak on the local filesystem",
		`{"nodes":{"leak":{"locked":{"type":"git","url":"file:///p"},"original":{"type":"git","url":"file:///p"}},"root":{"inputs":{"leak":"leak"}}},"root":"root"}`:                             "names input leak on the local filesystem",
		`{"nodes":{"leak":{"locked":{"type":"github","owner":"o","repo":"r"},"original":{"type":"github","owner":"o","repo":"r","path":"/p"}},"root":{"inputs":{"leak":"leak"}}},"root":"root"}`: "names input leak on the local filesystem",
		`not json`: "could not be parsed as a lock",
	}
	for lock, want := range cases {
		if got := lockNamesLocalInput([]byte(lock)); got != want {
			t.Errorf("lockNamesLocalInput(%s) = %q, want %q", lock, got, want)
		}
	}
}

// TestResolveFormatterPathViaNixReportsAMissingNix: with no gate pin and no nix
// on PATH, the real resolver names the cause rather than failing obscurely.
func TestResolveFormatterPathViaNixReportsAMissingNix(t *testing.T) {
	if _, err := exec.LookPath("nix"); err == nil {
		t.Skip("nix is on PATH; the missing-nix branch cannot be exercised here")
	}
	_, err := resolveFormatterPathViaNix(context.Background(), t.TempDir(), "0123456", "fmt")
	if err == nil || !strings.Contains(err.Error(), "nix is not on PATH") {
		t.Errorf("err = %v, want the missing-nix reason", err)
	}
}

// Guards against a regression where the repo root is derived from the
// worktree's own .git file rather than the shared common dir.
func TestRepoRootOfAWorktreeIsTheMainCheckout(t *testing.T) {
	testgit.RequireGit(t)
	repo := t.TempDir()
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")
	root, err := repoRootOf(wt)
	if err != nil {
		t.Fatal(err)
	}
	if !sameDir(t, root, repo) {
		t.Errorf("repoRootOf(%s) = %s, want %s", wt, root, repo)
	}
	if _, err := git.RevParse(root, "refs/heads/main"); err != nil {
		t.Errorf("the root must resolve main: %v", err)
	}
}
