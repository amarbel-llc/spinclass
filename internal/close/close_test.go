package close

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/nixgc"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/testgit"
	tap "github.com/amarbel-llc/tap/go/pkgs/writer"
)

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
