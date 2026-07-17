package close

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/nixgc"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/testgit"
	tap "github.com/amarbel-llc/tap/go/pkgs/writer"
)

// TestCloseImplicitRemovesStateNotCheckout verifies the implicit
// short-circuit in Run: a no-target close auto-detected from a repo's main
// checkout drops the implicit session's state-<randID>.json but leaves the
// checkout (and its tracked files) on disk.
func TestCloseImplicitRemovesStateNotCheckout(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repo := filepath.Join(t.TempDir(), "repo")
	testgit.MustInit(t, repo)

	// A tracked file in the main checkout — must survive the close.
	tracked := filepath.Join(repo, "keep.txt")
	if err := os.WriteFile(tracked, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	const randID = "abc12345"
	live := session.State{
		Kind:         session.KindImplicit,
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		RepoPath:     repo,
		WorktreePath: repo,
		Branch:       "master",
		SessionKey:   "repo/" + randID,
	}
	if err := session.WriteImplicit(live, randID); err != nil {
		t.Fatal(err)
	}

	// close.Run reads cwd via os.Getwd; chdir into the checkout so the
	// auto-detect short-circuit fires.
	t.Chdir(repo)

	if err := Run(io.Discard, "", false, nil, "tap", nil); err != nil {
		t.Fatalf("close.Run: %v", err)
	}

	// The implicit session state is gone.
	if got, gotRand, err := session.FindImplicitAtCwd(repo); err != nil || got != nil || gotRand != "" {
		t.Fatalf("implicit session still present after close: got (%v, %q, %v)", got, gotRand, err)
	}

	// The checkout and its tracked file remain — close did NOT remove them.
	if _, err := os.Stat(repo); err != nil {
		t.Fatalf("checkout dir wrongly removed: %v", err)
	}
	if _, err := os.Stat(tracked); err != nil {
		t.Fatalf("tracked file wrongly removed: %v", err)
	}
}

// TestRunNoImplicitSessionFallsThrough confirms the implicit short-circuit
// does NOT fire when cwd is a main checkout with no live implicit session:
// Run must fall through to resolveTarget (which errors here — no sessions,
// sessionpick.Choose returns "no sessions" before any picker) rather than
// short-circuiting to a nil-error no-op. Guards a future broadening of the
// short-circuit gate.
func TestRunNoImplicitSessionFallsThrough(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// A normal checkout: .git is a dir, so worktree.IsWorktree is false and
	// the implicit-detect branch is eligible — but no implicit state file is
	// written, so FindImplicitAtCwd returns nil and the short-circuit is
	// skipped.
	repo := filepath.Join(t.TempDir(), "repo")
	testgit.MustInit(t, repo)

	t.Chdir(repo)

	err := Run(io.Discard, "", false, nil, "tap", nil)
	if err == nil {
		t.Fatal("expected Run to reach resolveTarget and error, not short-circuit to nil")
	}
}

// TestResolveTargetByIDFindsSession is the happy path: a tracked
// session for a repo can be resolved by its worktree dirname even when
// resolveTarget is called from outside that worktree (cwd is the main
// repo).
func TestResolveTargetByIDFindsSession(t *testing.T) {
	testgit.RequireGit(t)
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repoPath := filepath.Join(root, "repo")
	wtPath := filepath.Join(repoPath, ".worktrees", "feature-x")
	testgit.MustInit(t, repoPath)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "feature-x")

	s := session.State{
		PID:          12345,
		SessionState: session.StateActive,
		RepoPath:     repoPath,
		WorktreePath: wtPath,
		Branch:       "feature-x",
		SessionKey:   "repo/feature-x",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}
	if err := session.Write(s); err != nil {
		t.Fatal(err)
	}

	gotRepo, gotWT, gotBranch, err := resolveTarget(repoPath, "feature-x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotRepo != repoPath {
		t.Errorf("repo = %q, want %q", gotRepo, repoPath)
	}
	if gotWT != wtPath {
		t.Errorf("wt = %q, want %q", gotWT, wtPath)
	}
	if gotBranch != "feature-x" {
		t.Errorf("branch = %q, want %q", gotBranch, "feature-x")
	}
}

// TestResolveTargetBySessionKey: the `<repo-dirname>/<branch>` keys
// `sc list` prints resolve cross-repo, from a cwd outside any git repo,
// and disambiguate worktree dirnames that collide across repos.
func TestResolveTargetBySessionKey(t *testing.T) {
	testgit.RequireGit(t)
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	for _, repo := range []string{"alpha", "beta"} {
		repoPath := filepath.Join(root, repo)
		wtPath := filepath.Join(repoPath, ".worktrees", "feature-x")
		testgit.MustInit(t, repoPath)
		testgit.MustWorktreeAdd(t, repoPath, wtPath, "feature-x")
		if err := session.Write(session.State{
			SessionState: session.StateInactive,
			RepoPath:     repoPath,
			WorktreePath: wtPath,
			Branch:       "feature-x",
			SessionKey:   repo + "/feature-x",
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// cwd outside any repo: an explicit target needs no repo detection.
	outside := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", outside)

	gotRepo, gotWT, gotBranch, err := resolveTarget(outside, "beta/feature-x", nil)
	if err != nil {
		t.Fatal(err)
	}
	wantRepo := filepath.Join(root, "beta")
	if gotRepo != wantRepo {
		t.Errorf("repo = %q, want %q", gotRepo, wantRepo)
	}
	if want := filepath.Join(wantRepo, ".worktrees", "feature-x"); gotWT != want {
		t.Errorf("wt = %q, want %q", gotWT, want)
	}
	if gotBranch != "feature-x" {
		t.Errorf("branch = %q, want %q", gotBranch, "feature-x")
	}

	// The ambiguous bare dirname surfaces the session keys, not the
	// "no spinclass session" hint.
	_, _, _, err = resolveTarget(outside, "feature-x", nil)
	if err == nil {
		t.Fatal("ambiguous bare target: expected error, got nil")
	}
	for _, key := range []string{"alpha/feature-x", "beta/feature-x"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("ambiguity error %q is missing session key %q", err, key)
		}
	}
	if strings.Contains(err.Error(), "no spinclass session") {
		t.Errorf("ambiguity error %q must not carry the not-found hint", err)
	}
}

// TestPlanNixGCOverrideMatrix locks the precedence contract on
// planNixGC: the explicit `--nix-gc=<bool>` flag (override != nil) wins
// over the sweatfile cascade's [hooks].disable-nix-gc, and a nil
// override defers to the sweatfile. See issue #57.
func TestPlanNixGCOverrideMatrix(t *testing.T) {
	t.Cleanup(restoreNixgcSeams())

	tBool := func(b bool) *bool { return &b }
	cases := []struct {
		name             string
		sweatfileDisable bool // value returned by nixgcDisabled
		override         *bool
		wantNewPlanCall  bool
		wantNilPlan      bool
	}{
		{"sweatfile-enabled, no override, plan runs", false, nil, true, false},
		{"sweatfile-disabled, no override, skip", true, nil, false, true},
		{"sweatfile-disabled, override=true, plan runs", true, tBool(true), true, false},
		{"sweatfile-enabled, override=false, skip", false, tBool(false), false, true},
		{"sweatfile-enabled, override=true, plan runs", false, tBool(true), true, false},
		{"sweatfile-disabled, override=false, skip", true, tBool(false), false, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			disabled := c.sweatfileDisable
			nixgcDisabled = func(string, string) bool { return disabled }

			var newPlanCalled bool
			nixgcNewPlan = func(string) (nixgc.Plan, error) {
				newPlanCalled = true
				return nixgc.Plan{
					WorktreePath: "/fake/wt",
					Roots:        []nixgc.Root{{LinkPath: "/fake/link", StorePath: "/nix/store/x"}},
					Closure:      []string{"/nix/store/x"},
				}, nil
			}

			got := planNixGC("/fake/repo", "/fake/wt", c.override)
			if newPlanCalled != c.wantNewPlanCall {
				t.Errorf("nixgcNewPlan called=%v, want=%v", newPlanCalled, c.wantNewPlanCall)
			}
			if (got == nil) != c.wantNilPlan {
				t.Errorf("plan nil=%v, want nil=%v", got == nil, c.wantNilPlan)
			}
		})
	}
}

// TestPlanNixGCNoRootsReturnsNil verifies the silent no-op path: when
// gc is enabled but the worktree has no gc roots, planNixGC drops the
// plan rather than threading an empty closure through to Reap.
func TestPlanNixGCNoRootsReturnsNil(t *testing.T) {
	t.Cleanup(restoreNixgcSeams())

	nixgcDisabled = func(string, string) bool { return false }
	nixgcNewPlan = func(string) (nixgc.Plan, error) {
		return nixgc.Plan{WorktreePath: "/fake/wt", Roots: nil}, nil
	}

	if got := planNixGC("/fake/repo", "/fake/wt", nil); got != nil {
		t.Errorf("expected nil plan when no roots, got %+v", got)
	}
}

// TestPlanNixGCNixUnavailableReturnsNil verifies that
// nixgc.ErrNixUnavailable from the plan-build step is treated as a
// silent no-op (matches the production behavior on machines without
// nix-store on PATH).
func TestPlanNixGCNixUnavailableReturnsNil(t *testing.T) {
	t.Cleanup(restoreNixgcSeams())

	nixgcDisabled = func(string, string) bool { return false }
	nixgcNewPlan = func(string) (nixgc.Plan, error) {
		return nixgc.Plan{}, nixgc.ErrNixUnavailable
	}

	if got := planNixGC("/fake/repo", "/fake/wt", nil); got != nil {
		t.Errorf("expected nil plan when nix is unavailable, got %+v", got)
	}
}

// TestPlanNixGCErrPlanTimedOutReturnsNil verifies that nixgc's plan
// timeout (issue #74) is also treated as a silent no-op so a stalled
// daemon does not break sc close.
func TestPlanNixGCErrPlanTimedOutReturnsNil(t *testing.T) {
	t.Cleanup(restoreNixgcSeams())

	nixgcDisabled = func(string, string) bool { return false }
	nixgcNewPlan = func(string) (nixgc.Plan, error) {
		return nixgc.Plan{}, nixgc.ErrPlanTimedOut
	}

	if got := planNixGC("/fake/repo", "/fake/wt", nil); got != nil {
		t.Errorf("expected nil plan when plan step timed out, got %+v", got)
	}
}

// restoreNixgcSeams snapshots the package-level seams and returns a
// restorer for `t.Cleanup`. Tests that override either seam must call
// this before mutating, otherwise leakage corrupts later subtests.
func restoreNixgcSeams() func() {
	disabledOrig := nixgcDisabled
	newPlanOrig := nixgcNewPlan
	return func() {
		nixgcDisabled = disabledOrig
		nixgcNewPlan = newPlanOrig
	}
}

// mustCommitFile adds and commits a new file in dir using the provided
// git identity already configured by testgit.SetHermeticEnv/MustInit.
func mustCommitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", dir, "add", name},
		{"-C", dir, "commit", "-m", msg},
	} {
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// setupNoTTYClose creates a worktree with one unintegrated commit and
// overrides closeInteractive to report non-TTY. Returns repoPath and wtPath.
func setupNoTTYClose(t *testing.T) (repoPath, wtPath string) {
	t.Helper()
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	repoPath = filepath.Join(root, "repo")
	wtPath = filepath.Join(repoPath, ".worktrees", "feature-x")
	testgit.MustInit(t, repoPath)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "feature-x")
	mustCommitFile(t, wtPath, "change.txt", "change", "unintegrated commit")
	orig := closeInteractive
	closeInteractive = func() bool { return false }
	t.Cleanup(func() { closeInteractive = orig })
	return
}

// TestRunResolvedNoTTYDirtyErrors is the regression guard for #222:
// RunResolved must immediately error (not block on huh.Confirm) when
// stdin is not a TTY and the branch has unintegrated commits.
// The error must name the unintegrated state and point at --force.
func TestRunResolvedNoTTYDirtyErrors(t *testing.T) {
	repoPath, wtPath := setupNoTTYClose(t)

	err := RunResolved(io.Discard, repoPath, wtPath, "feature-x", false, nil, "tap")
	if err == nil {
		t.Fatal("expected error on non-TTY with unintegrated commits, got nil")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error %q does not mention --force", err)
	}
	if _, statErr := os.Stat(wtPath); statErr != nil {
		t.Errorf("worktree was unexpectedly removed: %v", statErr)
	}
}

// TestRunResolvedNoTTYForceBypassesPrompt verifies that --force still
// closes the worktree when stdin is not a TTY: force skips the
// confirmation entirely so the TTY guard is never reached (#222).
func TestRunResolvedNoTTYForceBypassesPrompt(t *testing.T) {
	repoPath, wtPath := setupNoTTYClose(t)

	if err := RunResolved(io.Discard, repoPath, wtPath, "feature-x", true, nil, "tap"); err != nil {
		t.Fatalf("RunResolved with force=true: %v", err)
	}
	if _, statErr := os.Stat(wtPath); !os.IsNotExist(statErr) {
		t.Errorf("expected worktree to be removed, stat returned: %v", statErr)
	}
}

// TestResolveTargetByIDOrphanedWorktreeRejected covers the new contract:
// a git worktree without a spinclass state file is not a valid close
// target. The error must mention `git worktree remove` so users know
// the escape hatch.
func TestResolveTargetByIDOrphanedWorktreeRejected(t *testing.T) {
	testgit.RequireGit(t)
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repoPath := filepath.Join(root, "repo")
	wtPath := filepath.Join(repoPath, ".worktrees", "orphan")
	testgit.MustInit(t, repoPath)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "orphan")

	_, _, _, err := resolveTarget(repoPath, "orphan", nil)
	if err == nil {
		t.Fatal("expected error for orphaned worktree")
	}
	if !strings.Contains(err.Error(), "git worktree remove") {
		t.Errorf("error = %q, want contains 'git worktree remove'", err.Error())
	}
}

// TestRunReapEmptyClosureEmitsOk is the regression guard for #76:
// nixgc.Reap with an empty Closure is a no-op success, and the
// surrounding OutputBlock must render "ok" — not "not ok". Pre-fix,
// the success path returned a non-nil *yaml_diagnostic.YAMLDiagnostic carrying only
// summary extras, which tap-go interprets as "not ok".
func TestRunReapEmptyClosureEmitsOk(t *testing.T) {
	var buf bytes.Buffer
	tw := tap.NewWriter(&buf)
	plan := nixgc.Plan{WorktreePath: "/tmp/empty-wt"}
	runReap(tw, plan, "brave-myrtle")
	tw.Plan()

	out := buf.String()
	if strings.Contains(out, "not ok") {
		t.Errorf("TAP output unexpectedly contains 'not ok' for empty-closure reap:\n%s", out)
	}
	if !strings.Contains(out, "ok 1 - nix-gc reap brave-myrtle") {
		t.Errorf("TAP output missing 'ok 1 - nix-gc reap brave-myrtle':\n%s", out)
	}
	for _, want := range []string{"reclaimed: 0", "bytes_freed: 0", "closure: 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("TAP output missing summary line %q:\n%s", want, out)
		}
	}
}
