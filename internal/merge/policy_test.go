package merge

import (
	"errors"
	"fmt"
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

func commitFile(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-m", "add "+name)
	return runGit(t, dir, "rev-parse", "HEAD")
}

func TestPolicyAttestedUnchangedTipIsPlain(t *testing.T) {
	repoDir, wtPath := setupPolicyRepo(t, "")
	attested := runGit(t, wtPath, "rev-parse", "HEAD")

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{Gate: GateAttested, AttestedSha: attested})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if tr := policyPoint(t, recs); !tr.OK || tr.Description != policyLabel+": attested" {
		t.Errorf("policy point = %+v, want plain ok attested", tr)
	}
}

func TestPolicyAttestedNotesCommitsSince(t *testing.T) {
	repoDir, wtPath := setupPolicyRepo(t, "")
	attested := runGit(t, wtPath, "rev-parse", "HEAD")
	fix := commitFile(t, wtPath, "b.txt")

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{Gate: GateAttested, AttestedSha: attested})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	tr := policyPoint(t, recs)
	wantPrefix := policyLabel + ": attested; 1 commit since the attestation at " + shortSha(attested)
	if !tr.OK || !strings.HasPrefix(tr.Description, wantPrefix) || !strings.Contains(tr.Description, shortSha(fix)) {
		t.Errorf("policy point = %+v, want prefix %q naming %s", tr, wantPrefix, shortSha(fix))
	}
}

func TestPolicyAttestedRebaseAloneIsPlain(t *testing.T) {
	repoDir, wtPath := setupPolicyRepo(t, "")
	attested := runGit(t, wtPath, "rev-parse", "HEAD")
	commitFile(t, repoDir, "other.txt")

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{Gate: GateAttested, AttestedSha: attested})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if tr := policyPoint(t, recs); !tr.OK || tr.Description != policyLabel+": attested" {
		t.Errorf("policy point = %+v, want plain ok attested after a pure rebase", tr)
	}
	if pinned := runGit(t, wtPath, "rev-parse", "HEAD"); pinned == attested {
		t.Errorf("branch tip still %s: the rebase never rewrote it, so this test proves nothing", attested)
	}
}

func TestPolicyAttestedCapsListAtFive(t *testing.T) {
	repoDir, wtPath := setupPolicyRepo(t, "")
	attested := runGit(t, wtPath, "rev-parse", "HEAD")
	for i := 0; i < 6; i++ {
		commitFile(t, wtPath, fmt.Sprintf("c%d.txt", i))
	}

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{Gate: GateAttested, AttestedSha: attested})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	tr := policyPoint(t, recs)
	prefix := policyLabel + ": attested; 6 commits since the attestation at " + shortSha(attested) + ": "
	if !tr.OK || !strings.HasPrefix(tr.Description, prefix) || !strings.HasSuffix(tr.Description, " …") {
		t.Fatalf("policy point = %+v, want prefix %q and suffix ' …'", tr, prefix)
	}
	list := strings.TrimSuffix(strings.TrimPrefix(tr.Description, prefix), " …")
	if got := len(strings.Fields(list)); got != 5 {
		t.Errorf("listed %d shas (%q), want exactly 5", got, list)
	}
}

func TestPolicyAttestedMalformedShaNeverReachesGit(t *testing.T) {
	repoDir, wtPath := setupPolicyRepo(t, "")

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{Gate: GateAttested, AttestedSha: "--output=x"})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if tr := policyPoint(t, recs); !tr.OK || !strings.HasSuffix(tr.Description, "; not a commit sha") {
		t.Errorf("policy point = %+v, want the not-a-commit-sha cause", tr)
	}
}

func TestPolicyAttestedRebasePlusNewCommitCountsOne(t *testing.T) {
	repoDir, wtPath := setupPolicyRepo(t, "")
	attested := runGit(t, wtPath, "rev-parse", "HEAD")
	commitFile(t, repoDir, "other.txt")
	commitFile(t, wtPath, "b.txt")

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{Gate: GateAttested, AttestedSha: attested})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if tr := policyPoint(t, recs); !tr.OK || !strings.Contains(tr.Description, "1 commit since") {
		t.Errorf("policy point = %+v, want 1 commit since", tr)
	}
}

func TestPolicyAttestedUnknownShaSaysSo(t *testing.T) {
	repoDir, wtPath := setupPolicyRepo(t, "")

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{Gate: GateAttested, AttestedSha: "0123456789abcdef0123456789abcdef01234567"})
	if err != nil {
		t.Fatalf("merge must land despite an unknown attested sha: %v", err)
	}
	if tr := policyPoint(t, recs); !tr.OK || !strings.Contains(tr.Description, "could not compare with the attested commit 0123456789ab") {
		t.Errorf("policy point = %+v, want the could-not-compare label", tr)
	}
}

// An exemption-admitted merge must not fail open when the (branch-controlled)
// session hierarchy no longer shows the gate live by the time it lands, e.g. a
// queued merge whose branch dropped its skills: predicates still decide.
func TestPolicyNeedsExemptionFailsClosedOnDormantSessionGate(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature") // no skills anywhere
	before := runGit(t, repoDir, "rev-parse", "main")

	_, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{Gate: GateNeedsExemption})
	if !errors.Is(err, ErrAttestationNotExempt) {
		t.Fatalf("err = %v, want ErrAttestationNotExempt", err)
	}
	if after := runGit(t, repoDir, "rev-parse", "main"); after != before {
		t.Errorf("main moved %s -> %s; a refused merge must land nothing", before, after)
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
