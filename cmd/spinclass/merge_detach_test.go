package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/job"
)

// The blocking merge-this-session detaches into a job when it outlives
// mergeDetachAfter (spinclass: a client timeout used to lose the result of a
// merge that landed, and the sync path kept no job record or wake). A merge
// that finishes in time returns its result exactly as before; one that does
// not returns the job id at once and lands without the caller, with the
// completion wake as the record. Only under clown (mergeDetachEligible): the
// wake needs it, and without it the call stays inline.

// detachSeams makes the test's merges detach-eligible after d.
func detachSeams(t *testing.T, d time.Duration) {
	t.Helper()
	prevEligible, prevAfter := mergeDetachEligible, mergeDetachAfter
	mergeDetachEligible, mergeDetachAfter = func() bool { return true }, d
	t.Cleanup(func() { mergeDetachEligible, mergeDetachAfter = prevEligible, prevAfter })
}

func callMerge(t *testing.T) (text string, isErr bool) {
	t.Helper()
	res, err := handleMergeThisSession(context.Background(), json.RawMessage(`{"local_only":true}`), nil)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	return res.Text, res.IsErr
}

// A merge that finishes inside the window answers as the inline tool did: the
// final result, an error flag matching the outcome, the attestation consumed
// or released. It also leaves a job record, which is what makes the result
// recoverable when the caller is gone.
func TestDetachableMergeFinishingInTimeReturnsTheFinalResult(t *testing.T) {
	cwd, repoPath, branch := gatedWorktreeFixture(t, "[hooks]\npre-merge = \"true\"\n\n"+gateSweat)
	commitFile(t, cwd, "x.txt")
	detachSeams(t, 2*time.Minute)

	text, isErr := callMerge(t)
	if isErr || strings.Contains(text, "still running") {
		t.Fatalf("a fast merge must return its result, got isErr=%v %q", isErr, text)
	}
	if got := readAttestation(t, repoPath, branch); got != nil {
		t.Errorf("the landed merge must consume the attestation, got %+v", got)
	}
	j, err := job.Read(cwd)
	if err != nil || j.Kind != job.KindMerge || j.Status != job.StatusSucceeded {
		t.Errorf("job record = %+v, %v; want a succeeded merge job", j, err)
	}
}

func TestDetachableMergeThatFailsInTimeReportsTheFailureAndKeepsTheAttestation(t *testing.T) {
	cwd, repoPath, branch := gatedWorktreeFixture(t, "[hooks]\npre-merge = \"false\"\n\n"+gateSweat)
	commitFile(t, cwd, "x.txt")
	detachSeams(t, 2*time.Minute)

	text, isErr := callMerge(t)
	if !isErr || strings.Contains(text, "still running") {
		t.Fatalf("a red hook must fail the merge in time, got isErr=%v %q", isErr, text)
	}
	a := readAttestation(t, repoPath, branch)
	if a == nil || a.Claim != nil {
		t.Fatalf("a failed merge leaves the attestation buffered and unclaimed, got %+v", a)
	}
	if j, err := job.Read(cwd); err != nil || j.Status != job.StatusFailed {
		t.Errorf("job record = %+v, %v; want a failed job", j, err)
	}
}

// A merge that outlives the window returns the job id and a do-not-re-run
// instruction, as a non-error result; the attestation stays claimed while it
// runs, and the merge then lands without the caller: consumed, job succeeded.
func TestDetachableMergePastTheWindowReturnsAJobAndLandsWithoutTheCaller(t *testing.T) {
	cwd, repoPath, branch := gatedWorktreeFixture(t, "[hooks]\npre-merge = \"sleep 2\"\n\n"+gateSweat)
	commitFile(t, cwd, "x.txt")
	detachSeams(t, 150*time.Millisecond)

	start := time.Now()
	text, isErr := callMerge(t)
	if took := time.Since(start); took > time.Second {
		t.Errorf("the call took %v; it must return once the window passes", took)
	}
	if isErr {
		t.Fatalf("a detached merge is not an error, got %q", text)
	}
	j, err := job.Read(cwd)
	if err != nil || j.Status != job.StatusRunning {
		t.Fatalf("job record = %+v, %v; want a running job", j, err)
	}
	for _, want := range []string{"still running", j.ID, "Do NOT re-run", "wake"} {
		if !strings.Contains(text, want) {
			t.Errorf("result lacks %q: %q", want, text)
		}
	}
	if a := readAttestation(t, repoPath, branch); a == nil || a.Claim == nil {
		t.Fatalf("the attestation must stay claimed while the merge runs, got %+v", a)
	}

	<-job.WaitDone(cwd)
	if got := readAttestation(t, repoPath, branch); got != nil {
		t.Errorf("the merge landed without the caller and must consume the attestation, got %+v", got)
	}
	if j, err := job.Read(cwd); err != nil || j.Status != job.StatusSucceeded {
		t.Errorf("job record after = %+v, %v; want succeeded", j, err)
	}
}

// A detached merge that fails releases the attestation for the retry, as the
// inline tool did.
func TestDetachedMergeThatFailsReleasesTheAttestation(t *testing.T) {
	cwd, repoPath, branch := gatedWorktreeFixture(t, "[hooks]\npre-merge = \"sleep 1; false\"\n\n"+gateSweat)
	commitFile(t, cwd, "x.txt")
	detachSeams(t, 100*time.Millisecond)

	if text, isErr := callMerge(t); isErr || !strings.Contains(text, "still running") {
		t.Fatalf("want a detached result, got isErr=%v %q", isErr, text)
	}
	<-job.WaitDone(cwd)
	a := readAttestation(t, repoPath, branch)
	if a == nil || a.Claim != nil {
		t.Fatalf("a failed detached merge must leave the attestation buffered and unclaimed, got %+v", a)
	}
	if j, err := job.Read(cwd); err != nil || j.Status != job.StatusFailed {
		t.Errorf("job record = %+v, %v; want failed", j, err)
	}
}

// A background job already running for the session refuses the call before
// the attestation is claimed (the same discipline as the async tool).
func TestDetachableMergeRefusesWhileAJobRunsWithoutClaiming(t *testing.T) {
	cwd, repoPath, branch := gatedWorktreeFixture(t, "[hooks]\npre-merge = \"true\"\n\n"+gateSweat)
	commitFile(t, cwd, "x.txt")
	detachSeams(t, 2*time.Minute)
	release := startBlockingJob(t, cwd)
	t.Cleanup(func() { close(release); <-job.WaitDone(cwd) })

	text, isErr := callMerge(t)
	if !isErr || !strings.Contains(text, "already running") {
		t.Fatalf("want the already-running refusal, got isErr=%v %q", isErr, text)
	}
	if a := readAttestation(t, repoPath, branch); a == nil || a.Claim != nil {
		t.Fatalf("a refused call must not claim the attestation, got %+v", a)
	}
}

// Not eligible (no clown, so no wake) or a zero window: the call stays inline
// and leaves no job record, exactly as before.
func TestMergeStaysInlineWhenDetachIsOffOrIneligible(t *testing.T) {
	for name, set := range map[string]func(){
		"zero window": func() { detachSeams(t, 0) },
		"ineligible": func() {
			prevEligible, prevAfter := mergeDetachEligible, mergeDetachAfter
			mergeDetachEligible, mergeDetachAfter = func() bool { return false }, time.Millisecond
			t.Cleanup(func() { mergeDetachEligible, mergeDetachAfter = prevEligible, prevAfter })
		},
	} {
		t.Run(name, func(t *testing.T) {
			cwd, _, _ := gatedWorktreeFixture(t, "[hooks]\npre-merge = \"sleep 1\"\n\n"+gateSweat)
			commitFile(t, cwd, "x.txt")
			set()
			text, isErr := callMerge(t)
			if isErr || strings.Contains(text, "still running") {
				t.Fatalf("an inline merge returns its result, got isErr=%v %q", isErr, text)
			}
			if _, err := job.Read(cwd); err == nil {
				t.Error("an inline merge leaves no job record")
			}
		})
	}
}
