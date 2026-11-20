package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// Stopping a session's agent and leaving it stopped
// (docs/plans/2026-11-20-stop-start-rights.md): a stop is a RestartRequest of
// kind stop, queued through the restart hand-off, and the preflight answers
// the verdict "stop" to a frontend that names it in CLOWN_RESTART_VERDICTS.

const stoppingClownVerdicts = "restart,none,refused,stop"

func callStopThis(t *testing.T, args string) (text string, isErr bool) {
	t.Helper()
	res, err := handleStopThisSession(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("handleStopThisSession: %v", err)
	}
	return res.Text, res.IsErr
}

func callStopChild(t *testing.T, args string) (text string, isErr bool) {
	t.Helper()
	res, err := handleStopChildSession(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("handleStopChildSession: %v", err)
	}
	return res.Text, res.IsErr
}

func TestStopRightsAreHandleRights(t *testing.T) {
	set, err := session.ParseHandleRights("stop,stop-now,start,park")
	if err != nil {
		t.Fatalf("ParseHandleRights: %v", err)
	}
	for _, r := range []string{"stop", "stop-now", "start", "park"} {
		if !set.Has(r) {
			t.Errorf("right %s missing", r)
		}
	}
	def, _ := session.ParseHandleRights(session.DefaultHandleRights)
	for _, r := range []string{"stop", "stop-now", "start", "park"} {
		if def.Has(r) {
			t.Errorf("the default grant must not carry %s", r)
		}
	}
}

func TestStopThisSessionQueuesAStopThroughTheHandoff(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartSelfFixture(t, true)

	text, isErr := callStopThis(t, `{"reason":"done for now"}`)
	if isErr || !strings.Contains(text, "stop") {
		t.Fatalf("want a queued stop, got isErr=%v %q", isErr, text)
	}
	req := readKid(t, repoPath).RestartRequest
	if req == nil || req.Kind != session.RequestKindStop || req.When != "idle" || req.Reason != "done for now" {
		t.Fatalf("request = %+v, want an idle stop with the reason", req)
	}
	if len(*calls) != 1 {
		t.Fatalf("the restart hand-off pokes the frontend once: %+v", *calls)
	}
}

func TestStopChildNeedsTheStopRight(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartChildFixture(t, "holder", session.State{
		Holders:      []string{"holder"},
		HandleRights: map[string]string{"holder": "observe,restart"},
	})
	text, isErr := callStopChild(t, `{"child":"worker/kid"}`)
	if !isErr || !strings.Contains(text, "stop-child-session needs stop") {
		t.Fatalf("want a refusal naming the stop right, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
		t.Error("a refused stop must record and run nothing")
	}
}

func TestStopChildWithTheStopRightQueuesAStop(t *testing.T) {
	stubRestartHandoff(t, "", nil)
	repoPath, _ := restartChildFixture(t, "holder", session.State{
		Holders:      []string{"holder"},
		HandleRights: map[string]string{"holder": "observe,stop"},
	})
	if text, isErr := callStopChild(t, `{"child":"worker/kid"}`); isErr {
		t.Fatalf("a holder with stop must be allowed: %q", text)
	}
	req := readKid(t, repoPath).RestartRequest
	if req == nil || req.Kind != session.RequestKindStop || req.Requester != "holder" {
		t.Fatalf("request = %+v, want a stop by holder", req)
	}
}

func TestStopChildNowNeedsTheStopNowRight(t *testing.T) {
	stubRestartHandoff(t, "", nil)
	repoPath, _ := restartChildFixture(t, "holder", session.State{
		Holders:      []string{"holder"},
		HandleRights: map[string]string{"holder": "observe,stop"},
	})
	text, isErr := callStopChild(t, `{"child":"worker/kid","when":"now"}`)
	if !isErr || !strings.Contains(text, "needs stop-now") {
		t.Fatalf("want a refusal naming stop-now, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).RestartRequest != nil {
		t.Error("a refused stop must record nothing")
	}
}

func TestStopChildNowWithTheStopNowRightQueuesANowStop(t *testing.T) {
	stubRestartHandoff(t, "", nil)
	repoPath, _ := restartChildFixture(t, "holder", session.State{
		Holders:      []string{"holder"},
		HandleRights: map[string]string{"holder": "observe,stop,stop-now"},
	})
	if text, isErr := callStopChild(t, `{"child":"worker/kid","when":"now"}`); isErr {
		t.Fatalf("a holder with stop-now must be allowed: %q", text)
	}
	if req := readKid(t, repoPath).RestartRequest; req == nil || req.When != "now" || req.Kind != session.RequestKindStop {
		t.Fatalf("request = %+v, want a now stop", req)
	}
}

// queuedStop queues an idle self-stop for the preflight.
func queuedStop(t *testing.T) (repoPath string, watch *[][]string) {
	t.Helper()
	stubRestartHandoff(t, "", nil)
	stubNotifyPrincipal(t)
	watch = stubScheduleRestartWatch(t, nil)
	repoPath, _ = restartSelfFixture(t, true)
	if text, isErr := callStopThis(t, `{"reason":"pause"}`); isErr {
		t.Fatalf("setup: stop refused: %q", text)
	}
	return repoPath, watch
}

func TestPreflightOfAStopIsAStopVerdict(t *testing.T) {
	repoPath, watch := queuedStop(t)
	t.Setenv("CLOWN_RESTART_VERDICTS", stoppingClownVerdicts)

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "stop" || v.When != "idle" {
		t.Fatalf("verdict = %+v, %v; want stop/idle", v, err)
	}
	if v.Posture != "" || v.Mode != "" {
		t.Errorf("a stop carries no posture or mode: %+v", v)
	}
	st := readKid(t, repoPath)
	if st.RestartRequest != nil {
		t.Error("the stop request is consumed")
	}
	if st.StoppedAt == nil || st.StoppedReason != "pause" || st.StoppedBy != selfPrincipal {
		t.Errorf("stopped mark = %v %q %q, want set with the reason and requester", st.StoppedAt, st.StoppedReason, st.StoppedBy)
	}
	if st.RestartingKind != session.RequestKindStop {
		t.Errorf("RestartingKind = %q, want stop so a re-arm stays a stop", st.RestartingKind)
	}
	if len(*watch) != 0 {
		t.Errorf("no relaunch is expected after a stop, so no watchdog: %+v", *watch)
	}
}

func TestPreflightOfAStopIsRefusedWhenTheFrontendCannotStop(t *testing.T) {
	repoPath, _ := queuedStop(t)
	t.Setenv("CLOWN_RESTART_VERDICTS", "")

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "refused" || !strings.Contains(v.Reason, "cannot stop on request") {
		t.Fatalf("verdict = %+v, %v; want refused naming the missing capability", v, err)
	}
	st := readKid(t, repoPath)
	if st.RestartRequest != nil {
		t.Error("a refused stop is cleared, so it is not retried forever")
	}
	if st.StoppedAt != nil {
		t.Error("a refused stop must not mark the session stopped")
	}
}

func TestRearmOfAStopStaysAStop(t *testing.T) {
	queuedStop(t)
	t.Setenv("CLOWN_RESTART_VERDICTS", stoppingClownVerdicts)
	if v, err := runRestartPreflight("worker/kid"); err != nil || v.Verdict != "stop" {
		t.Fatalf("setup preflight = %+v, %v", v, err)
	}

	v, err := runRestartRearm("worker/kid")
	if err != nil || v.Verdict != "stop" {
		t.Fatalf("rearm = %+v, %v; want stop", v, err)
	}
}
