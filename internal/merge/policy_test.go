package merge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/ndjsoncrap"
)

// A live gate: the global sweatfile (outside any repo, so never
// branch-controlled) declares a pre-merge skill.
func writeGlobalSkills(t *testing.T) {
	t.Helper()
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".config", "spinclass")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sweatfile"),
		[]byte("[[pre-merge-skills]]\nname = \"review\"\nrationale = \"Mandatory.\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// commitSweatfile commits content as the tracked repo sweatfile in dir.
func commitSweatfile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "sweatfile"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "sweatfile")
	runGit(t, dir, "commit", "-m", "sweatfile")
}

// setupPolicyRepo commits baseSweatfile (if any) to main, then cuts a feature
// worktree with one commit touching only a.txt.
func setupPolicyRepo(t *testing.T, baseSweatfile string) (repoDir, wtPath string) {
	t.Helper()
	repoDir = setupRepo(t)
	writeGlobalSkills(t)
	if baseSweatfile != "" {
		commitSweatfile(t, repoDir, baseSweatfile)
	}
	wtPath = setupWorktree(t, repoDir, "feature")
	if err := os.WriteFile(filepath.Join(wtPath, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "a.txt")
	runGit(t, wtPath, "commit", "-m", "feature commit")
	return repoDir, wtPath
}

func policyPoint(t *testing.T, recs []ndjsoncrap.Record) ndjsoncrap.Test {
	t.Helper()
	tests := testRecords(recs)
	tr, ok := findTest(tests, policyLabel)
	if !ok {
		t.Fatalf("no %q point in %v", policyLabel, testDescs(tests))
	}
	return tr
}

// The predicate is handed the landing facts and runs in a base-tree checkout:
// this one exempts any diff that leaves file.txt untouched.
const fileTxtUntouched = "[[pre-merge-exemptions]]\nname = \"not-file-txt\"\ncommand = 'git diff --quiet \"$SPINCLASS_MERGE_BASE\" \"$SPINCLASS_LANDING_SHA\" -- file.txt'\n"

func TestPolicyExemptionFromBaseLands(t *testing.T) {
	repoDir, wtPath := setupPolicyRepo(t, fileTxtUntouched)

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{Gate: GateNeedsExemption})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	tr := policyPoint(t, recs)
	if !tr.OK || !strings.Contains(tr.Description, "exempt (not-file-txt)") {
		t.Errorf("policy point = %+v, want ok exempt (not-file-txt)", tr)
	}
	if got := runGit(t, repoDir, "log", "-1", "--format=%s", "main"); got != "feature commit" {
		t.Errorf("main tip = %q, want the landed feature commit", got)
	}
}

// The trust rule: an exemption that exists only on the branch being merged
// cannot vouch for its own merge.
func TestPolicyBranchCannotVouchForItself(t *testing.T) {
	repoDir, wtPath := setupPolicyRepo(t, "")
	commitSweatfile(t, wtPath, "[[pre-merge-exemptions]]\nname = \"yes\"\ncommand = \"true\"\n")
	before := runGit(t, repoDir, "rev-parse", "main")

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{Gate: GateNeedsExemption})
	if !errors.Is(err, ErrAttestationNotExempt) {
		t.Fatalf("err = %v, want ErrAttestationNotExempt", err)
	}
	if tr := policyPoint(t, recs); tr.OK {
		t.Errorf("policy point unexpectedly ok: %+v", tr)
	}
	if after := runGit(t, repoDir, "rev-parse", "main"); after != before {
		t.Errorf("main moved %s -> %s; a refused merge must land nothing", before, after)
	}
}

func TestPolicyDeclinedPredicateFailsWithVerdict(t *testing.T) {
	repoDir, wtPath := setupPolicyRepo(t, "[[pre-merge-exemptions]]\nname = \"never\"\ncommand = \"exit 3\"\n")

	_, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{Gate: GateNeedsExemption})
	if !errors.Is(err, ErrAttestationNotExempt) || !strings.Contains(err.Error(), "never (exit 3)") {
		t.Fatalf("err = %v, want not-exempt naming never (exit 3)", err)
	}
}

// Terminal merges are always exempt (operator decision, #326): predicates are
// never consulted — even one that would decline — and the bypass is recorded.
func TestPolicyTerminalRecordsBypass(t *testing.T) {
	repoDir, wtPath := setupPolicyRepo(t, "[[pre-merge-exemptions]]\nname = \"never\"\ncommand = \"exit 3\"\n")

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{})
	if err != nil {
		t.Fatalf("terminal merge: %v", err)
	}
	tr := policyPoint(t, recs)
	if tr.Directive == nil || tr.Directive.Kind != "skip" || tr.Directive.Reason != PolicyTerminalBypassReason {
		t.Errorf("policy point = %+v, want the terminal-bypass skip", tr)
	}
}

func TestPolicyAttestedRecordsAttested(t *testing.T) {
	repoDir, wtPath := setupPolicyRepo(t, "")

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{Gate: GateAttested})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if tr := policyPoint(t, recs); !tr.OK || tr.Description != policyLabel+": attested" {
		t.Errorf("policy point = %+v, want ok attested", tr)
	}
}

// A dormant gate (no skills anywhere) emits no policy point on any path.
func TestPolicyDormantGateEmitsNothing(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, ok := findTest(testRecords(recs), policyLabel); ok {
		t.Error("dormant gate emitted a policy point")
	}
}
