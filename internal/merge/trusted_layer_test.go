package merge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// During a merge the REPO layer of the sweatfile hierarchy must come from the
// sweatfile COMMITTED on the default branch tip: never from the session
// branch's own commits, and never from the main checkout's live, uncommitted
// <repo>/sweatfile. These sentinel tests commit a trusted body on main, a
// hostile one on the session branch, and then overwrite the main checkout's
// live file with a third; each body touches its own marker so the test can say
// which one actually ran.

// trustedLayerMarkers names the three marker files, one per source of repo
// layer content.
type trustedLayerMarkers struct{ good, session, live string }

func newTrustedLayerMarkers(t *testing.T) trustedLayerMarkers {
	t.Helper()
	dir := t.TempDir()
	return trustedLayerMarkers{
		good:    filepath.Join(dir, "GOOD"),
		session: filepath.Join(dir, "SESSION"),
		live:    filepath.Join(dir, "LIVE"),
	}
}

func markerExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// assertOnlyCommittedDefaultBranchRan fails unless the marker of the sweatfile
// committed on the default branch exists and the session's and the live file's
// do not.
func (m trustedLayerMarkers) assertOnlyCommittedDefaultBranchRan(t *testing.T) {
	t.Helper()
	if !markerExists(m.good) {
		t.Errorf("the sweatfile committed on the default branch did not run (%s missing)", m.good)
	}
	if markerExists(m.session) {
		t.Errorf("a command from the SESSION branch's committed sweatfile ran (%s exists)", m.session)
	}
	if markerExists(m.live) {
		t.Errorf("a command from the main checkout's LIVE uncommitted sweatfile ran (%s exists)", m.live)
	}
}

// trustedLayerRepo commits body(good) as main's sweatfile, cuts a feature
// worktree whose branch commits body(session) as its own sweatfile (plus the
// a.txt feature commit), then overwrites the main checkout's sweatfile on disk,
// uncommitted, with body(live).
func trustedLayerRepo(t *testing.T, m trustedLayerMarkers, body func(marker string) string) (repoDir, wtPath string) {
	t.Helper()
	repoDir = setupRepo(t)
	commitSweatfile(t, repoDir, body(m.good))
	wtPath = setupWorktree(t, repoDir, "feature")
	commitFeature(t, wtPath)
	commitSweatfile(t, wtPath, body(m.session))
	writeLiveRepoSweatfile(t, repoDir, body(m.live))
	return repoDir, wtPath
}

func TestMergePreMergeHookComesFromCommittedDefaultBranch(t *testing.T) {
	m := newTrustedLayerMarkers(t)
	repoDir, wtPath := trustedLayerRepo(t, m, func(marker string) string {
		return "[hooks]\npre-merge = \"touch " + marker + "\"\n"
	})

	// The landing may legitimately fail on the dirty live file; which hook ran is
	// what is under test.
	if _, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{}); err != nil {
		t.Logf("merge: %v", err)
	}
	m.assertOnlyCommittedDefaultBranchRan(t)
}

func TestMergeRepairComesFromCommittedDefaultBranch(t *testing.T) {
	m := newTrustedLayerMarkers(t)
	repoDir, wtPath := trustedLayerRepo(t, m, func(marker string) string {
		return "[hooks]\nrepair = \"touch " + marker + "\"\n"
	})

	if _, _, err := runPrepare(t, repoDir, wtPath, "feature"); err != nil {
		t.Logf("PrepareMerge: %v", err)
	}
	m.assertOnlyCommittedDefaultBranchRan(t)
}

// disable-merge committed on the default branch refuses the merge even when the
// session branch's sweatfile and the main checkout's live file both say false.
func TestMergeDisableMergeComesFromCommittedDefaultBranch(t *testing.T) {
	m := newTrustedLayerMarkers(t)
	repoDir, wtPath := trustedLayerRepo(t, m, func(marker string) string {
		// Only the default branch's committed body disables the merge.
		if marker == m.good {
			return "[hooks]\ndisable-merge = true\n"
		}
		return "[hooks]\ndisable-merge = false\n"
	})

	_, _, err := runPrepare(t, repoDir, wtPath, "feature")
	if err == nil || !strings.Contains(err.Error(), "merge disabled") {
		t.Fatalf("err = %v, want the committed default branch's disable-merge to refuse the merge", err)
	}
}

// A required [[pre-merge-skills]] entry committed on the default branch stays
// required when the session branch's sweatfile or the main checkout's live file
// drops it. A live gate shows up as the terminal-bypass policy point.
func TestMergePreMergeSkillsComeFromCommittedDefaultBranch(t *testing.T) {
	const required = "[[pre-merge-skills]]\nname = \"review\"\nrationale = \"Mandatory.\"\n"
	const dropped = "[hooks]\n"

	assertGateLive := func(t *testing.T, repoDir, wtPath string) {
		t.Helper()
		recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{})
		if err != nil {
			t.Logf("merge: %v", err)
		}
		tr := policyPoint(t, recs)
		if tr.Directive == nil || tr.Directive.Reason != PolicyTerminalBypassReason {
			t.Errorf("policy point = %+v, want the terminal bypass: the committed default branch requires a skill", tr)
		}
	}

	t.Run("session commit drops the skill", func(t *testing.T) {
		repoDir := setupRepo(t)
		commitSweatfile(t, repoDir, required)
		wtPath := setupWorktree(t, repoDir, "feature")
		commitFeature(t, wtPath)
		commitSweatfile(t, wtPath, dropped)
		assertGateLive(t, repoDir, wtPath)
	})

	t.Run("live edit drops the skill", func(t *testing.T) {
		repoDir := setupRepo(t)
		commitSweatfile(t, repoDir, required)
		wtPath := setupWorktree(t, repoDir, "feature")
		commitFeature(t, wtPath)
		writeLiveRepoSweatfile(t, repoDir, dropped)
		assertGateLive(t, repoDir, wtPath)
	})
}

// Known residual, not to be fixed: post-merge runs from the sweatfile of the
// commit that LANDED (#300), which is the session's own commit.
func TestMergePostMergeCannotRunSessionBranchCommand(t *testing.T) {
	t.Skip("residual: post-merge runs from the landed sha by design (#300); see FDR 0035/0018 residual")

	m := newTrustedLayerMarkers(t)
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	commitSweatfile(t, wtPath, kroneTarget("touch "+m.session))

	if _, err := runFinish(t, repoDir, wtPath, "feature", false); err != nil {
		t.Logf("merge: %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("merge did not land; the probe proves nothing")
	}
	if markerExists(m.session) {
		t.Errorf("a [[post-merge]] command from the SESSION branch's sweatfile ran (%s exists)", m.session)
	}
}
