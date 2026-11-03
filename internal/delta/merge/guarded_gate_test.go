package merge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
	"code.linenisgreat.com/spinclass/internal/charlie/check"
)

// guardedMarkers are the files each command of a guarded-merge fixture writes,
// so a test can say which of them ran.
type guardedMarkers struct{ repoHook, repair, repoGate, gate, pwd string }

// guardedFixture commits a repo sweatfile whose pre-merge, repair and (ignored)
// confined-pre-merge each write a marker (plus extra), and a global sweatfile
// whose confined-pre-merge writes a marker and the directory it ran in.
func guardedFixture(t *testing.T, extra string) (repoDir, wtPath string, m guardedMarkers) {
	t.Helper()
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	t.Cleanup(hookrun.SetConfinedGateTools("git", "sh"))
	repoDir, wtPath = setupPostMergeRepo(t, "feature")
	dir := t.TempDir()
	m = guardedMarkers{
		repoHook: filepath.Join(dir, "repo-hook"),
		repair:   filepath.Join(dir, "repair"),
		repoGate: filepath.Join(dir, "repo-gate"),
		gate:     filepath.Join(dir, "gate"),
		pwd:      filepath.Join(dir, "pwd"),
	}
	writeRepoSweatfile(t, repoDir, "[hooks]\npre-merge = \": > "+m.repoHook+"\"\nrepair = \": > "+m.repair+"\"\nconfined-pre-merge = \": > "+m.repoGate+"\"\n"+extra)
	writeGlobalSweatfile(t, "[hooks]\nconfined-pre-merge = \": > "+m.gate+"; pwd > "+m.pwd+"\"\n")
	return repoDir, wtPath, m
}

func (m guardedMarkers) assertOnlyTheGateRan(t *testing.T) {
	t.Helper()
	if !markerExists(m.gate) {
		t.Error("the trusted guarded gate did not run")
	}
	if markerExists(m.repoHook) {
		t.Error("the repo's pre-merge command ran: a guarded merge must not execute it")
	}
	if markerExists(m.repair) {
		t.Error("the repo's repair command ran: a guarded merge runs no repair")
	}
	if markerExists(m.repoGate) {
		t.Error("the repo layer's confined-pre-merge ran: only layers above the repo may set it")
	}
}

// A guarded merge (spinclass#449) runs the trusted gate in the build worktree
// and neither the repo's pre-merge nor its repair, on the queued and the
// unqueued landing path alike, and the record says so.
func TestGuardedMergeRunsTheTrustedGateAndNothingOfTheRepos(t *testing.T) {
	for _, tc := range []struct{ name, extra string }{
		{"queued", ""},
		{"unqueued", "disable-merge-queue = true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoDir, wtPath, m := guardedFixture(t, tc.extra)
			recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{GuardedGate: true})
			if err != nil {
				t.Fatalf("merge: %v", err)
			}
			if !landed(t, repoDir) {
				t.Fatal("the guarded merge did not land")
			}
			m.assertOnlyTheGateRan(t)
			pwd, rerr := os.ReadFile(m.pwd)
			if rerr != nil {
				t.Fatal(rerr)
			}
			wantPrefix := filepath.Join(repoDir, ".worktrees", check.BuildWorktreePrefix)
			if got := strings.TrimSpace(string(pwd)); !strings.HasPrefix(got, wantPrefix) {
				t.Errorf("the gate ran in %q, want the build worktree under %q", got, wantPrefix)
			}
			tests := testRecords(recs)
			if tr, ok := findTest(tests, "guarded gate feature"); !ok || !tr.OK {
				t.Errorf("guarded gate point = %+v, want ok", tr)
			}
			if tr, ok := findTest(tests, "guarded gate feature: ignored"); !ok || !tr.OK {
				t.Errorf("no point records the ignored repo-level confined-pre-merge: %v", testDescs(tests))
			}
			if tr, ok := findTest(tests, "repair feature (skipped"); !ok || !tr.OK {
				t.Errorf("repair point = %+v, want the skip recorded", tr)
			}
		})
	}
}

// A guarded merge never runs its gate in the session worktree: with the build
// worktree turned off on the default branch it is refused and nothing lands.
func TestGuardedMergeRefusesWithoutTheBuildWorktree(t *testing.T) {
	repoDir, wtPath, m := guardedFixture(t, "disable-merge-build-worktree = true\n")
	mainBefore := runGit(t, repoDir, "rev-parse", "main")
	_, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{GuardedGate: true})
	if err == nil || !strings.Contains(err.Error(), "build worktree") {
		t.Fatalf("err = %v, want a refusal naming the build worktree", err)
	}
	if runGit(t, repoDir, "rev-parse", "main") != mainBefore {
		t.Error("the default branch moved: nothing may land")
	}
	if markerExists(m.gate) || markerExists(m.repoHook) {
		t.Error("a gate ran although the merge was refused")
	}
}

// A session the frontend launched confined is guarded without any option: the
// unit record is the evidence, and it lives outside the worktree.
func TestLaunchedConfinedSessionGetsTheGuardedGate(t *testing.T) {
	repoDir, wtPath, m := guardedFixture(t, "")
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	record := confinement.UnitRecordPath(stateDir, wtPath)
	if err := os.MkdirAll(filepath.Dir(record), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runFinish(t, repoDir, wtPath, "feature", false); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("the guarded merge did not land")
	}
	m.assertOnlyTheGateRan(t)
}

// Without the option and without a unit record the ordinary gate runs, so
// the guard changes nothing for an unconfined session.
func TestUnguardedMergeRunsTheRepoHook(t *testing.T) {
	repoDir, wtPath, m := guardedFixture(t, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := runFinish(t, repoDir, wtPath, "feature", false); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !markerExists(m.repoHook) || markerExists(m.gate) {
		t.Errorf("unguarded: repo hook ran=%v gate ran=%v, want true/false", markerExists(m.repoHook), markerExists(m.gate))
	}
}

// With no trusted hierarchy to choose the gate from, a guarded merge is
// refused rather than landing with no gate at all.
func TestGuardedMergeFailsClosedWithoutATrustedHierarchy(t *testing.T) {
	repoDir, wtPath, m := guardedFixture(t, "")
	mainBefore := runGit(t, repoDir, "rev-parse", "main")
	t.Setenv("HOME", "")
	_, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{GuardedGate: true})
	if err == nil || !strings.Contains(err.Error(), "guarded gate") {
		t.Fatalf("err = %v, want a guarded gate refusal", err)
	}
	if runGit(t, repoDir, "rev-parse", "main") != mainBefore {
		t.Error("the default branch moved: nothing may land")
	}
	if markerExists(m.repoHook) || markerExists(m.gate) || markerExists(m.repair) {
		t.Error("a command ran although the merge was refused")
	}
}
