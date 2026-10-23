package merge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// A merge whose landed post-merge section differs from the one on the default
// branch tip is refused before landing unless the operator confirmed the new
// section's digest. These tests cover the section/digest, the two checks in
// PrepareMerge and FinishMerge, and the accepted and unchanged paths.

// acceptHeadSection is the options an operator's --accept-post-merge would
// produce for the section committed at wtPath's HEAD.
func acceptHeadSection(t *testing.T, repoDir, wtPath string) PostMergeOptions {
	t.Helper()
	return PostMergeOptions{AcceptPostMergeDigest: headSectionDigest(t, repoDir, wtPath)}
}

func headSectionDigest(t *testing.T, repoDir, wtPath string) string {
	t.Helper()
	s, err := PostMergeSectionAt(repoDir, runGit(t, wtPath, "rev-parse", "HEAD"))
	if err != nil {
		t.Fatalf("PostMergeSectionAt: %v", err)
	}
	return s.Digest()
}

func strp(s string) *string { return &s }

func TestPostMergeSectionDigestFormat(t *testing.T) {
	d := PostMergeSectionOf(sweatfile.Sweatfile{}).Digest()
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(d) {
		t.Errorf("digest = %q, want sha256:<64 hex>", d)
	}
}

func TestPostMergeSectionDigestIgnoresUnrelatedSweatfileFields(t *testing.T) {
	targets := []sweatfile.PostMergeTarget{{Name: "krone", Command: "echo krone", Verify: strp("true")}}
	a := sweatfile.Sweatfile{PostMerge: targets}
	yes := true
	b := sweatfile.Sweatfile{PostMerge: targets, Hooks: &sweatfile.Hooks{DisableMerge: &yes}}
	if da, db := PostMergeSectionOf(a).Digest(), PostMergeSectionOf(b).Digest(); da != db {
		t.Errorf("digest changed with an unrelated field: %s != %s", da, db)
	}
}

func TestPostMergeSectionDigestIgnoresRemovalSentinels(t *testing.T) {
	a := sweatfile.Sweatfile{PostMerge: []sweatfile.PostMergeTarget{{Name: "krone", Command: "echo krone"}}}
	b := sweatfile.Sweatfile{PostMerge: []sweatfile.PostMergeTarget{{Name: "krone", Command: "echo krone"}, {Name: "gone"}}}
	if PostMergeSectionOf(a).Digest() != PostMergeSectionOf(b).Digest() {
		t.Error("a name-only removal sentinel runs nothing and must not change the digest")
	}
}

func TestPostMergeSectionDigestCoversEveryField(t *testing.T) {
	base := func() sweatfile.Sweatfile {
		return sweatfile.Sweatfile{
			PostMerge: []sweatfile.PostMergeTarget{{Name: "krone", Command: "echo krone", Verify: strp("true")}},
			Hooks:     &sweatfile.Hooks{PostMerge: strp("echo legacy"), PostMergeTimeout: strp("5m")},
		}
	}
	baseDigest := PostMergeSectionOf(base()).Digest()
	yes := true
	mutations := map[string]func(*sweatfile.Sweatfile){
		"target command": func(s *sweatfile.Sweatfile) { s.PostMerge[0].Command = "echo other" },
		"target verify":  func(s *sweatfile.Sweatfile) { s.PostMerge[0].Verify = strp("false") },
		"target name":    func(s *sweatfile.Sweatfile) { s.PostMerge[0].Name = "flic" },
		"extra target": func(s *sweatfile.Sweatfile) {
			s.PostMerge = append(s.PostMerge, sweatfile.PostMergeTarget{Name: "flic", Command: "echo flic"})
		},
		"legacy hook": func(s *sweatfile.Sweatfile) { s.Hooks.PostMerge = strp("echo other") },
		"timeout":     func(s *sweatfile.Sweatfile) { s.Hooks.PostMergeTimeout = strp("6m") },
		"disable":     func(s *sweatfile.Sweatfile) { s.Hooks.DisablePostMerge = &yes },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := base()
			mutate(&s)
			if PostMergeSectionOf(s).Digest() == baseDigest {
				t.Errorf("digest unchanged after changing %s", name)
			}
		})
	}
}

func TestPostMergeSectionChangesFromSummary(t *testing.T) {
	tip := PostMergeSectionOf(sweatfile.Sweatfile{
		PostMerge: []sweatfile.PostMergeTarget{
			{Name: "kept", Command: "a"}, {Name: "gone", Command: "b"}, {Name: "edited", Command: "c"},
		},
	})
	merged := PostMergeSectionOf(sweatfile.Sweatfile{
		PostMerge: []sweatfile.PostMergeTarget{
			{Name: "kept", Command: "a"}, {Name: "new", Command: "d"}, {Name: "edited", Command: "c2"},
		},
		Hooks: &sweatfile.Hooks{PostMerge: strp("echo legacy")},
	})
	got := merged.ChangesFrom(tip)
	if strings.Contains(got, "\n") {
		t.Errorf("summary must be one line: %q", got)
	}
	for _, want := range []string{"added: new", "removed: gone", "changed: edited", "legacy hook changed"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q lacks %q", got, want)
		}
	}
}

// changedSectionRepo cuts a feature worktree whose branch adds a krone
// [[post-merge]] target running `touch marker`; the default branch has none.
func changedSectionRepo(t *testing.T, marker string) (repoDir, wtPath string) {
	t.Helper()
	repoDir, wtPath = setupPostMergeRepo(t, "feature")
	commitSweatfile(t, wtPath, kroneTarget("touch "+marker))
	return repoDir, wtPath
}

func TestMergeRefusesChangedPostMergeSectionBeforeLanding(t *testing.T) {
	marker := t.TempDir() + "/RAN"
	repoDir, wtPath := changedSectionRepo(t, marker)
	mainBefore := runGit(t, repoDir, "rev-parse", "main")

	recs, err := runFinish(t, repoDir, wtPath, "feature", false)
	if err == nil {
		t.Fatal("a merge that adds a post-merge target must be refused without the operator's digest")
	}
	if landed(t, repoDir) || runGit(t, repoDir, "rev-parse", "main") != mainBefore {
		t.Error("the default branch moved: nothing may land")
	}
	if markerExists(marker) {
		t.Error("the session's post-merge command ran")
	}
	tr, ok := findTest(testRecords(recs), "post-merge section feature")
	if !ok || tr.OK {
		t.Fatalf("want a failing 'post-merge section feature' point, got %+v", testDescs(testRecords(recs)))
	}
}

func TestMergeRefusalMessageNamesDigestsChangeAndOperatorCommands(t *testing.T) {
	repoDir, wtPath := changedSectionRepo(t, t.TempDir()+"/RAN")
	tipDigest := PostMergeSectionOf(sweatfile.Sweatfile{}).Digest()
	newDigest := headSectionDigest(t, repoDir, wtPath)

	_, err := runFinish(t, repoDir, wtPath, "feature", false)
	if err == nil {
		t.Fatal("want a refusal")
	}
	msg := err.Error()
	for _, want := range []string{
		tipDigest,
		newDigest,
		"targets added: krone",
		"sc merge --show-post-merge feature",
		"sc merge --accept-post-merge " + newDigest + " feature",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "does not match") {
		t.Errorf("no digest was given, so the message must not claim a mismatch:\n%s", msg)
	}
}

func TestMergeWrongPostMergeDigestRefusedBeforeLanding(t *testing.T) {
	marker := t.TempDir() + "/RAN"
	repoDir, wtPath := changedSectionRepo(t, marker)
	newDigest := headSectionDigest(t, repoDir, wtPath)
	wrong := PostMergeSectionOf(sweatfile.Sweatfile{PostMerge: []sweatfile.PostMergeTarget{{Name: "x", Command: "y"}}}).Digest()
	tipDigest := PostMergeSectionOf(sweatfile.Sweatfile{}).Digest()

	_, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{AcceptPostMergeDigest: wrong})
	if err == nil {
		t.Fatal("a wrong digest must be refused")
	}
	if landed(t, repoDir) || markerExists(marker) {
		t.Error("nothing may land or run on a wrong digest")
	}
	msg := err.Error()
	for _, want := range []string{"does not match", wrong, newDigest, tipDigest} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal lacks %q:\n%s", want, msg)
		}
	}
}

func TestMergeCorrectPostMergeDigestLandsAndRunsNewSection(t *testing.T) {
	marker := t.TempDir() + "/RAN"
	repoDir, wtPath := changedSectionRepo(t, marker)
	pm := acceptHeadSection(t, repoDir, wtPath)

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, pm)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("an accepted merge must land")
	}
	if !markerExists(marker) {
		t.Error("the accepted section's command did not run")
	}
	tr, ok := findTest(testRecords(recs), "post-merge section feature")
	if !ok || !tr.OK || !strings.Contains(tr.Description, pm.AcceptPostMergeDigest) {
		t.Errorf("want an ok 'post-merge section' point naming the digest, got %+v (ok=%v)", tr, ok)
	}
}

// PrepareMerge refuses early; FinishMerge is authoritative on its own: a digest
// that satisfied PrepareMerge is not carried to FinishMerge.
func TestFinishMergeChecksPostMergeSectionAuthoritatively(t *testing.T) {
	marker := t.TempDir() + "/RAN"
	repoDir, wtPath := changedSectionRepo(t, marker)
	pm := acceptHeadSection(t, repoDir, wtPath)

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	pinned, err := PrepareMerge(context.Background(), ts, repoDir, wtPath, "feature", "main", false, pm)
	if err != nil {
		t.Fatalf("PrepareMerge with the digest: %v", err)
	}
	_, err = FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature", "main", pinned, false, true, nil, PostMergeOptions{})
	ts.Finish()
	if err == nil {
		t.Fatal("FinishMerge without the digest must refuse")
	}
	if landed(t, repoDir) || markerExists(marker) {
		t.Error("nothing may land or run")
	}
}

func TestPrepareMergeRefusesChangedPostMergeSectionEarly(t *testing.T) {
	repoDir, wtPath := changedSectionRepo(t, t.TempDir()+"/RAN")
	_, tests, err := runPrepare(t, repoDir, wtPath, "feature")
	if err == nil {
		t.Fatal("PrepareMerge must refuse before the slow gate")
	}
	if tr, ok := findTest(tests, "post-merge section feature"); !ok || tr.OK {
		t.Errorf("want a failing 'post-merge section' point, got %v", testDescs(tests))
	}
}

func TestMergeRefusesChangedPostMergeSectionOnUnqueuedPath(t *testing.T) {
	marker := t.TempDir() + "/RAN"
	repoDir, wtPath := changedSectionRepo(t, marker)
	writeGlobalSweatfile(t, "[hooks]\ndisable-merge-queue = true\n")

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	pinned, err := PrepareMerge(context.Background(), ts, repoDir, wtPath, "feature", "main", false, acceptHeadSection(t, repoDir, wtPath))
	if err != nil {
		t.Fatalf("PrepareMerge: %v", err)
	}
	_, err = FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature", "main", pinned, false, true, nil, PostMergeOptions{})
	ts.Finish()
	if err == nil {
		t.Fatal("the unqueued path must refuse a changed section without the digest")
	}
	if landed(t, repoDir) || markerExists(marker) {
		t.Error("nothing may land or run")
	}
}

// quietStdout points os.Stdout at a file for the test: RunContext renders its
// reporter there.
func quietStdout(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	real := os.Stdout
	os.Stdout = f
	t.Cleanup(func() { os.Stdout = real })
}

// --show-post-merge: both digests and both sections, nothing merged.
func TestRunContextShowPostMergePrintsBothSectionsAndMergesNothing(t *testing.T) {
	marker := t.TempDir() + "/RAN"
	repoDir, wtPath := changedSectionRepo(t, marker)
	newDigest := headSectionDigest(t, repoDir, wtPath)
	tipDigest := PostMergeSectionOf(sweatfile.Sweatfile{}).Digest()
	t.Chdir(wtPath)
	quietStdout(t)

	var out bytes.Buffer
	if err := RunContext(context.Background(), &mockExecutor{}, "ndjson", "", false, PostMergeOptions{ShowPostMerge: &out}); err != nil {
		t.Fatalf("--show-post-merge: %v", err)
	}
	for _, want := range []string{tipDigest, newDigest, "krone", "touch " + marker, "--accept-post-merge " + newDigest} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if landed(t, repoDir) || markerExists(marker) {
		t.Error("the dry run merged or ran something")
	}
}

// --accept-post-merge prints the exact section before merging, then merges.
func TestRunContextAcceptPostMergePrintsSectionThenMerges(t *testing.T) {
	marker := t.TempDir() + "/RAN"
	repoDir, wtPath := changedSectionRepo(t, marker)
	pm := acceptHeadSection(t, repoDir, wtPath)
	var out bytes.Buffer
	pm.AnnounceAccept = &out
	t.Chdir(wtPath)
	quietStdout(t)

	if err := RunContext(context.Background(), &mockExecutor{}, "ndjson", "", false, pm); err != nil {
		t.Fatalf("merge: %v", err)
	}
	for _, want := range []string{pm.AcceptPostMergeDigest, "krone", "touch " + marker} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("announced section lacks %q:\n%s", want, out.String())
		}
	}
	if !landed(t, repoDir) || !markerExists(marker) {
		t.Error("the accepted merge did not land and run")
	}
}

// An unchanged section behaves exactly as before, for every target, with no
// section point in the stream.
func TestMergeUnchangedPostMergeSectionRunsEveryTarget(t *testing.T) {
	names := []string{"krone", "flic", "nikulin", "linenisgreat"}
	var body strings.Builder
	for _, n := range names {
		fmt.Fprintf(&body, "[[post-merge]]\nname = %q\ncommand = %q\n\n", n, "echo ran-"+n)
	}
	repoDir := setupRepo(t)
	writeRepoSweatfile(t, repoDir, body.String())
	wtPath := setupWorktree(t, repoDir, "feature")
	commitFeature(t, wtPath)

	recs, err := runFinish(t, repoDir, wtPath, "feature", false)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("merge did not land")
	}
	for _, n := range names {
		node, ok := findNode(recs, "post-merge "+n)
		if !ok || !node.exitOK || !strings.Contains(node.output, "ran-"+n) {
			t.Errorf("target %s did not run ok: found=%v node=%+v", n, ok, node)
		}
	}
	if _, found := findTest(testRecords(recs), "post-merge section"); found {
		t.Error("an unchanged section must emit no post-merge section point")
	}
}
