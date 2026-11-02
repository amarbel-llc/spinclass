package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// `restart-preflight --rearm` (the follow-up clown's turn-cancel retry asked
// for): a restart the frontend dropped after the preflight consumed it, because
// a turn started during its countdown, is re-armed instead of asked afresh (a
// second plain preflight answers "none"). It re-runs everything a preflight
// checks plus the requester's restart right, supersedes the in-flight stamp
// (so the old watchdog stays silent and a new one is armed), and is bounded by
// a count and by the age of the first verdict.

// consumedRestart queues a self-restart and consumes it with the preflight, the
// state a dropped-by-clown restart leaves: no request, an in-flight stamp.
func consumedRestart(t *testing.T) (repoPath, wtPath string, watch *[][]string, notified *[]notifyCall) {
	t.Helper()
	watch = stubScheduleRestartWatch(t, nil)
	notified = stubNotifyPrincipal(t)
	repoPath, wtPath = requestRestart(t)
	if v, err := runRestartPreflight("worker/kid"); err != nil || v.Verdict != "restart" {
		t.Fatalf("setup preflight = %+v, %v; want restart", v, err)
	}
	return repoPath, wtPath, watch, notified
}

func TestRearmReissuesTheVerdictAndSupersedesTheStamp(t *testing.T) {
	repoPath, _, watch, notified := consumedRestart(t)
	before := readKid(t, repoPath)
	firstAt := *before.RestartingAt
	if before.RestartingFirstAt == nil || !before.RestartingFirstAt.Equal(firstAt) {
		t.Fatalf("the first preflight must record RestartingFirstAt = RestartingAt, got %v vs %v", before.RestartingFirstAt, firstAt)
	}
	startWakes, watchdogs := len(*notified), len(*watch)

	time.Sleep(2 * time.Millisecond)
	v, err := runRestartRearm("worker/kid")
	if err != nil || v.Verdict != "restart" || v.When != "idle" {
		t.Fatalf("rearm = %+v, %v; want restart/idle", v, err)
	}
	after := readKid(t, repoPath)
	if after.RestartRearms != 1 {
		t.Errorf("RestartRearms = %d, want 1", after.RestartRearms)
	}
	if !after.RestartingAt.After(firstAt) {
		t.Errorf("the stamp must move forward: %v then %v", firstAt, after.RestartingAt)
	}
	if after.RestartingFirstAt == nil || !after.RestartingFirstAt.Equal(firstAt) {
		t.Errorf("RestartingFirstAt must stay the first verdict's: %v want %v", after.RestartingFirstAt, firstAt)
	}
	if after.RestartRequest != nil {
		t.Error("a re-arm queues no request")
	}
	if len(*notified) != startWakes {
		t.Errorf("holders were already told it is restarting; a re-arm must not wake them again: %+v", *notified)
	}
	if len(*watch) != watchdogs+1 {
		t.Errorf("a re-arm arms a new watchdog for the new stamp: %d then %d", watchdogs, len(*watch))
	}

	// The old watchdog, armed for the first stamp, finds a newer one and stays silent.
	if err := runRestartWatch("worker/kid", firstAt.Format(time.RFC3339Nano)); err != nil || len(*notified) != startWakes {
		t.Errorf("the superseded watchdog must be silent: err %v, notifications %+v", err, *notified)
	}
}

func TestRearmWithNothingInFlightIsNone(t *testing.T) {
	stubNotifyPrincipal(t)
	restartSelfFixture(t, true)
	v, err := runRestartRearm("worker/kid")
	if err != nil || v.Verdict != "none" || !strings.Contains(v.Reason, "in flight") {
		t.Fatalf("got %+v, %v; want none naming that nothing is in flight", v, err)
	}
	if v, err := runRestartRearm("nope/nothing"); err != nil || v.Verdict != "none" {
		t.Fatalf("unknown session: got %+v, %v; want none", v, err)
	}
}

func TestRearmDefersToANewerQueuedRequest(t *testing.T) {
	repoPath, _, _, _ := consumedRestart(t)
	seedRequestBy(t, repoPath, selfPrincipal)
	v, err := runRestartRearm("worker/kid")
	if err != nil || v.Verdict != "none" || !strings.Contains(v.Reason, "queued") {
		t.Fatalf("got %+v, %v; want none naming the queued request", v, err)
	}
	if readKid(t, repoPath).RestartRequest == nil || readKid(t, repoPath).RestartRearms != 0 {
		t.Error("a newer request is the plain preflight's to consume; the re-arm must touch nothing")
	}
}

func TestRearmIsNoneOnceTheSessionReturnedOrIsClosing(t *testing.T) {
	for name, mutate := range map[string]func(*session.State){
		"returned": func(st *session.State) { n := time.Now().UTC().Add(time.Second); st.RestartedAt = &n },
		"closing":  func(st *session.State) { n := time.Now().UTC(); st.ClosingAt = &n },
	} {
		t.Run(name, func(t *testing.T) {
			repoPath, _, _, _ := consumedRestart(t)
			st := readKid(t, repoPath)
			mutate(st)
			if err := session.Write(*st); err != nil {
				t.Fatal(err)
			}
			v, err := runRestartRearm("worker/kid")
			if err != nil || v.Verdict != "none" {
				t.Fatalf("got %+v, %v; want none", v, err)
			}
			if readKid(t, repoPath).RestartRearms != 0 {
				t.Error("nothing may be re-armed")
			}
		})
	}
}

// Bounded twice, on the authority side so a frontend that retries forever is
// still stopped: by count, and by the age of the first verdict.
func TestRearmIsBounded(t *testing.T) {
	t.Run("by count", func(t *testing.T) {
		repoPath, _, _, _ := consumedRestart(t)
		for i := 1; i <= maxRestartRearms; i++ {
			if v, err := runRestartRearm("worker/kid"); err != nil || v.Verdict != "restart" {
				t.Fatalf("rearm %d = %+v, %v; want restart", i, v, err)
			}
		}
		v, err := runRestartRearm("worker/kid")
		if err != nil || v.Verdict != "none" || !strings.Contains(v.Reason, "times") {
			t.Fatalf("one over the bound = %+v, %v; want none naming the count", v, err)
		}
		if got := readKid(t, repoPath).RestartRearms; got != maxRestartRearms {
			t.Errorf("RestartRearms = %d, want it left at %d", got, maxRestartRearms)
		}
	})
	t.Run("by age", func(t *testing.T) {
		repoPath, _, _, _ := consumedRestart(t)
		st := readKid(t, repoPath)
		old := time.Now().UTC().Add(-restartRearmWindow - time.Minute)
		st.RestartingFirstAt = &old
		if err := session.Write(*st); err != nil {
			t.Fatal(err)
		}
		v, err := runRestartRearm("worker/kid")
		if err != nil || v.Verdict != "none" || !strings.Contains(v.Reason, "window") {
			t.Fatalf("got %+v, %v; want none naming the window", v, err)
		}
	})
}

// The checks the plain preflight makes are made again: the whole reason the
// re-arm exists is that the frontend's retry of the old verdict skipped them.
func TestRearmRefusesWhatThePreflightWouldRefuse(t *testing.T) {
	t.Run("a job started since", func(t *testing.T) {
		repoPath, wtPath, _, notified := consumedRestart(t)
		writeRunningJob(t, wtPath)
		n := len(*notified)
		v, err := runRestartRearm("worker/kid")
		if err != nil || v.Verdict != "refused" || !strings.Contains(v.Reason, "job") {
			t.Fatalf("got %+v, %v; want refused naming the job", v, err)
		}
		if readKid(t, repoPath).RestartRearms != 0 {
			t.Error("a refused re-arm is not counted or stamped")
		}
		if len(*notified) != n+1 {
			t.Errorf("the holders are told the re-arm was refused: %+v", *notified)
		}
	})
	t.Run("the hand-off was removed", func(t *testing.T) {
		consumedRestart(t)
		writeGlobalSweatfile(t, os.Getenv("HOME"), "")
		v, err := runRestartRearm("worker/kid")
		if err != nil || v.Verdict != "refused" || !strings.Contains(v.Reason, "[session-entry]") {
			t.Fatalf("got %+v, %v; want refused naming [session-entry]", v, err)
		}
	})
}

// The requester's restart right is checked at request time only; a re-arm is
// where it is checked again, so a right revoked in between is honoured.
func TestRearmRechecksTheRequestersRestartRight(t *testing.T) {
	setup := func(t *testing.T) (repoPath string) {
		t.Helper()
		stubScheduleRestartWatch(t, nil)
		stubNotifyPrincipal(t)
		stubRestartHandoff(t, "", nil)
		repoPath, _ = restartChildFixture(t, "holder", session.State{
			Holders:      []string{"holder"},
			HandleRights: map[string]string{"holder": "observe,restart"},
		})
		if text, isErr := callRestartChild(t, `{"child":"worker/kid"}`); isErr {
			t.Fatalf("setup request refused: %q", text)
		}
		if v, err := runRestartPreflight("worker/kid"); err != nil || v.Verdict != "restart" {
			t.Fatalf("setup preflight = %+v, %v", v, err)
		}
		return repoPath
	}

	t.Run("still held", func(t *testing.T) {
		setup(t)
		if v, err := runRestartRearm("worker/kid"); err != nil || v.Verdict != "restart" {
			t.Fatalf("got %+v, %v; want restart", v, err)
		}
	})
	t.Run("the right was revoked", func(t *testing.T) {
		repoPath := setup(t)
		st := readKid(t, repoPath)
		st.HandleRights = map[string]string{"holder": "observe"}
		if err := session.Write(*st); err != nil {
			t.Fatal(err)
		}
		v, err := runRestartRearm("worker/kid")
		if err != nil || v.Verdict != "refused" || !strings.Contains(v.Reason, "restart") {
			t.Fatalf("got %+v, %v; want refused naming the restart right", v, err)
		}
		if readKid(t, repoPath).RestartRearms != 0 {
			t.Error("a refused re-arm is not counted")
		}
	})
	t.Run("the handle was released", func(t *testing.T) {
		repoPath := setup(t)
		st := readKid(t, repoPath)
		st.Holders = []string{"someone-else"}
		if err := session.Write(*st); err != nil {
			t.Fatal(err)
		}
		v, err := runRestartRearm("worker/kid")
		if err != nil || v.Verdict != "refused" {
			t.Fatalf("got %+v, %v; want refused", v, err)
		}
	})
}
