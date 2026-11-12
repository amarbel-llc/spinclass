package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/job"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

func callSessionJobs(t *testing.T, args string) (string, bool) {
	t.Helper()
	res, err := handleSessionJobs(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("handleSessionJobs: %v", err)
	}
	return res.Text, res.IsErr
}

// writeKidJob writes worker/kid's job record and log as its serve would.
func writeKidJob(t *testing.T, wtPath string, j job.Job, log string) {
	t.Helper()
	if err := job.Write(wtPath, &j); err != nil {
		t.Fatal(err)
	}
	if log != "" {
		if err := os.WriteFile(job.LogPath(wtPath), []byte(log), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func jobsHolder(t *testing.T) (repoPath, wtPath string) {
	t.Helper()
	stubNotifyPrincipal(t)
	return restartChildFixture(t, "holder", session.State{
		Holders:      []string{"holder"},
		HandleRights: map[string]string{"holder": "observe,jobs"},
	})
}

func TestSessionJobsReportsStatusResultAndTail(t *testing.T) {
	_, wt := jobsHolder(t)
	start := time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)
	end := start.Add(7 * time.Minute)
	writeKidJob(t, wt, job.Job{
		ID: "merge-abc", Kind: job.KindMerge, Status: job.StatusFailed, StartedAt: start, EndedAt: &end,
		ResultText: "merge failed: test-nix", ClownJobID: "merge-abc",
	}, "one\ntwo\nthree\n")

	text, isErr := callSessionJobs(t, `{"child":"worker/kid","tail":2}`)
	if isErr {
		t.Fatalf("refused: %s", text)
	}
	for _, want := range []string{"worker/kid: merge job merge-abc failed", "ended 2026-11-12T16:07:00Z (7m0s)", "merge failed: test-nix", "two\nthree"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "one") {
		t.Errorf("tail=2 returned more than two lines:\n%s", text)
	}
	lines := readHandleJournal(t)
	if len(lines) != 1 || lines[0].Act != "job-read" || lines[0].Child != "worker/kid" || lines[0].By != "holder" {
		t.Errorf("journal = %+v, want one job-read by holder", lines)
	}
}

func TestSessionJobsStatusOnlyIsNotJournaled(t *testing.T) {
	_, wt := jobsHolder(t)
	writeKidJob(t, wt, job.Job{ID: "check-1", Kind: job.KindCheck, Status: job.StatusSucceeded, StartedAt: time.Now().UTC()}, "secret-ish\n")
	text, isErr := callSessionJobs(t, `{"child":"worker/kid","tail":0}`)
	if isErr || strings.Contains(text, "secret-ish") {
		t.Fatalf("tail=0 must return status only: isErr=%v %q", isErr, text)
	}
	if lines := readHandleJournal(t); len(lines) != 0 {
		t.Errorf("a status-only read must not be journaled: %+v", lines)
	}
}

func TestSessionJobsWithoutAJob(t *testing.T) {
	jobsHolder(t)
	text, isErr := callSessionJobs(t, `{"child":"worker/kid"}`)
	if isErr || !strings.Contains(text, "has run no merge or check job") {
		t.Fatalf("got isErr=%v %q", isErr, text)
	}
}

// A default grant does not carry jobs.
func TestSessionJobsRefusedWithoutTheJobsRight(t *testing.T) {
	stubNotifyPrincipal(t)
	_, wt := restartChildFixture(t, "holder", session.State{
		Holders:      []string{"holder"},
		HandleRights: map[string]string{"holder": session.DefaultHandleRights},
	})
	writeKidJob(t, wt, job.Job{ID: "merge-x", Kind: job.KindMerge, Status: job.StatusFailed, StartedAt: time.Now().UTC()}, "log\n")
	text, isErr := callSessionJobs(t, `{"child":"worker/kid"}`)
	if !isErr || !strings.Contains(text, "session-jobs needs jobs") || strings.Contains(text, "merge-x") {
		t.Fatalf("want a refusal naming the jobs right and no job data, got isErr=%v %q", isErr, text)
	}
}

// A worker can plant a symlink at its job.log to make the holder's serve read
// one of the user's private files; the read refuses it.
func TestSessionJobsRefusesAPlantedSymlinkAndHardLink(t *testing.T) {
	_, wt := jobsHolder(t)
	writeKidJob(t, wt, job.Job{ID: "merge-y", Kind: job.KindMerge, Status: job.StatusFailed, StartedAt: time.Now().UTC()}, "")
	secret := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(secret, []byte("PRIVATE-KEY-MATERIAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, job.LogPath(wt)); err != nil {
		t.Fatal(err)
	}
	text, isErr := callSessionJobs(t, `{"child":"worker/kid"}`)
	if isErr || strings.Contains(text, "PRIVATE-KEY-MATERIAL") || !strings.Contains(text, "job.log not read") {
		t.Fatalf("symlinked log: isErr=%v %q", isErr, text)
	}

	if err := os.Remove(job.LogPath(wt)); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, job.LogPath(wt)); err != nil {
		t.Skipf("hard link not possible here: %v", err)
	}
	text, isErr = callSessionJobs(t, `{"child":"worker/kid"}`)
	if isErr || strings.Contains(text, "PRIVATE-KEY-MATERIAL") || !strings.Contains(text, "links") {
		t.Fatalf("hard-linked log: isErr=%v %q", isErr, text)
	}
}

func TestSessionJobsWaitPollsARunningJob(t *testing.T) {
	_, wt := jobsHolder(t)
	start := time.Now().UTC()
	running := job.Job{ID: "merge-w", Kind: job.KindMerge, Status: job.StatusRunning, ServePID: os.Getpid(), StartedAt: start}
	writeKidJob(t, wt, running, "")

	origSleep, origNow := sessionJobsSleep, sessionJobsNow
	t.Cleanup(func() { sessionJobsSleep, sessionJobsNow = origSleep, origNow })
	clock := start
	sessionJobsNow = func() time.Time { return clock }
	polls := 0
	sessionJobsSleep = func(d time.Duration) {
		clock = clock.Add(d)
		polls++
		if polls == 3 {
			end := clock
			done := running
			done.Status, done.EndedAt = job.StatusSucceeded, &end
			writeKidJob(t, wt, done, "")
		}
	}
	text, isErr := callSessionJobs(t, `{"child":"worker/kid","wait":60,"tail":0}`)
	if isErr || !strings.Contains(text, "merge-w succeeded") || polls != 3 {
		t.Fatalf("polls=%d isErr=%v %q", polls, isErr, text)
	}
}

func TestSessionJobsRejectsOutOfRangeArguments(t *testing.T) {
	jobsHolder(t)
	for _, args := range []string{`{"child":"worker/kid","tail":401}`, `{"child":"worker/kid","wait":301}`, `{"child":"worker/kid","wait":-1}`, `{}`} {
		if _, isErr := callSessionJobs(t, args); !isErr {
			t.Errorf("%s: want a refusal", args)
		}
	}
}
