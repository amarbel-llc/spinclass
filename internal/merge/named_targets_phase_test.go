package merge

import (
	"fmt"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/ndjsoncrap"

	"code.linenisgreat.com/spinclass/internal/git"
)

// landed reports whether the feature merge reached main (a.txt is present).
func landed(t *testing.T, repoDir string) bool {
	t.Helper()
	out, err := git.Run(repoDir, "show", "main:a.txt")
	return err == nil && strings.TrimSpace(out) == "a"
}

// A named target with a verify that passes reports ok, and the merge lands.
func TestNamedPostMergeTargetVerifyOK(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo triggered"
verify = "echo healthy"
`)
	tests, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil)
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("merge did not land")
	}
	tr, ok := findTest(tests, "post-merge krone")
	if !ok || !tr.OK {
		t.Fatalf("expected ok post-merge krone point, got %+v (all: %v)", tr, testDescs(tests))
	}
}

// A verify that fails is verdict=verify-failed — distinct from a command
// failure — and is non-fatal: the merge still lands.
func TestNamedPostMergeTargetVerifyFailed(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo triggered"
verify = "echo unhealthy >&2; exit 1"
`)
	tests, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil)
	if err != nil {
		t.Fatalf("a verify failure must not fail the merge, got %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("merge did not land")
	}
	tr, ok := findTest(tests, "post-merge krone")
	if !ok || tr.OK {
		t.Fatalf("expected not-ok post-merge krone point, got %+v", tr)
	}
	if v := fmt.Sprintf("%v", tr.Diagnostic["verdict"]); v != "verify-failed" {
		t.Errorf("verdict = %q, want verify-failed", v)
	}
	if sev := fmt.Sprintf("%v", tr.Diagnostic["severity"]); sev != "warn" {
		t.Errorf("severity = %q, want warn", sev)
	}
	if out := fmt.Sprintf("%v", tr.Diagnostic["output"]); !strings.Contains(out, "unhealthy") {
		t.Errorf("verify output not surfaced: %v", tr.Diagnostic)
	}
}

// A command failure is verdict=command-failed, and the verify is not reached.
func TestNamedPostMergeTargetCommandFailed(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo trigger-broke >&2; exit 2"
verify = "echo should-not-run"
`)
	tests, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil)
	if err != nil {
		t.Fatalf("a command failure must not fail the merge, got %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("merge did not land")
	}
	tr, ok := findTest(tests, "post-merge krone")
	if !ok || tr.OK {
		t.Fatalf("expected not-ok post-merge krone point, got %+v", tr)
	}
	if v := fmt.Sprintf("%v", tr.Diagnostic["verdict"]); v != "command-failed" {
		t.Errorf("verdict = %q, want command-failed", v)
	}
	if out := fmt.Sprintf("%v", tr.Diagnostic["output"]); strings.Contains(out, "should-not-run") {
		t.Errorf("verify must not run after a command failure: %v", tr.Diagnostic)
	}
}

// Named targets supersede the legacy [hooks].post-merge string: with both
// configured, only the named target runs (FDR 0026).
func TestNamedTargetsSupersedeLegacyString(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[hooks]
post-merge = "echo LEGACY-RAN"

[[post-merge]]
name = "krone"
command = "echo krone-deployed"
`)
	tests, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil)
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if _, ok := findTest(tests, "post-merge krone"); !ok {
		t.Fatalf("expected the named target to run: %v", testDescs(tests))
	}
	// The legacy string's point is labeled "post-merge feature ..." — it must
	// NOT be present, since named targets supersede it.
	if tr, ok := findTest(tests, "post-merge feature"); ok {
		t.Errorf("legacy [hooks].post-merge must be superseded, got %+v", tr)
	}
}

// A targets selection runs only the named subset; unselected targets do not run.
func TestPostMergeSelectionSubset(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo krone"

[[post-merge]]
name = "nikulin"
command = "echo nikulin"
`)
	tests, err := runFinishTargets(t, repoDir, wtPath, "feature", false, []string{"krone"})
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if _, ok := findTest(tests, "post-merge krone"); !ok {
		t.Errorf("krone should have run: %v", testDescs(tests))
	}
	if tr, ok := findTest(tests, "post-merge nikulin"); ok {
		t.Errorf("nikulin should NOT have run under targets=[krone], got %+v", tr)
	}
}

// An empty selection deploys nothing (a docs-only merge), yet still lands.
func TestPostMergeSelectionNoneSkipsAll(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo krone"
`)
	tests, err := runFinishTargets(t, repoDir, wtPath, "feature", false, []string{})
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("an empty selection must still land the merge")
	}
	if tr, ok := findTest(tests, "post-merge "); ok {
		t.Errorf("empty selection must run no post-merge target, got %+v", tr)
	}
}

// Selecting a name no target declares fails the merge BEFORE it lands — a typo
// must not silently skip the intended deploy and land anyway (FDR 0026).
func TestPostMergeSelectionUnknownFailsPreLanding(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo krone"
`)
	mainBefore := runGit(t, repoDir, "rev-parse", "main")
	tests, err := runFinishTargets(t, repoDir, wtPath, "feature", false, []string{"kron"})
	if err == nil {
		t.Fatal("an unknown post-merge target must fail the merge")
	}
	if mainAfter := runGit(t, repoDir, "rev-parse", "main"); mainAfter != mainBefore {
		t.Errorf("merge must not land on an unknown-target selection: main moved %s -> %s", mainBefore, mainAfter)
	}
	tr, ok := findTest(tests, "post-merge selection")
	if !ok || tr.OK {
		t.Fatalf("expected a failing 'post-merge selection' point, got %+v (%v)", tr, testDescs(tests))
	}
	if msg := fmt.Sprintf("%v", tr.Diagnostic); !strings.Contains(msg, "kron") {
		t.Errorf("selection error should name the unknown target: %v", tr.Diagnostic)
	}
}

// Multiple targets run in declaration order, and a half-failure (one target
// fails) is non-fatal: the merge lands and every target reports its own verdict.
func TestNamedTargetsHalfFailureIsNonFatal(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo krone-ok"

[[post-merge]]
name = "nikulin"
command = "echo nikulin-broke >&2; exit 1"
`)
	tests, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil)
	if err != nil {
		t.Fatalf("a half-failing multi-target phase must not fail the merge, got %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("merge did not land")
	}
	krone, kok := findTest(tests, "post-merge krone")
	nikulin, nok := findTest(tests, "post-merge nikulin")
	if !kok || !krone.OK {
		t.Errorf("krone should be ok: %+v", krone)
	}
	if !nok || nikulin.OK {
		t.Errorf("nikulin should be not-ok: %+v", nikulin)
	}
	// Declaration order: krone's point precedes nikulin's in the stream.
	ki, ni := indexOf(tests, "post-merge krone"), indexOf(tests, "post-merge nikulin")
	if ki < 0 || ni < 0 || ki > ni {
		t.Errorf("targets should run in declaration order (krone before nikulin): krone@%d nikulin@%d", ki, ni)
	}
}

func indexOf(tests []ndjsoncrap.Test, prefix string) int {
	for i, tr := range tests {
		if strings.HasPrefix(tr.Description, prefix) {
			return i
		}
	}
	return -1
}
