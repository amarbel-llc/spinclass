package main

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// cancel-restart answers only what it did (#497). Once restart-preflight has
// approved a restart the frontend may already be stopping the agent, so a
// cancel can no longer withdraw it: it says so as an error instead of
// "nothing queued" or a success, and it stops any re-arm of that restart.

func TestCancelRestartAfterThePreflightIsTooLate(t *testing.T) {
	repoPath, _, _, _ := consumedRestart(t)

	text, isErr := callCancel(t, `{}`)
	if !isErr || !strings.Contains(text, "too late") {
		t.Fatalf("want an error saying it is too late, got isErr=%v %q", isErr, text)
	}
	if !strings.Contains(text, "worker/kid") {
		t.Errorf("the answer must name the session: %q", text)
	}
	if readKid(t, repoPath).RestartCancelledAt == nil {
		t.Error("a too-late cancel must still be recorded, so a re-arm refuses")
	}
}

// The preflight consuming the request between cancel's read and its locked
// write must not leave cancel reporting a success it did not have.
func TestCancelRestartThatLosesTheRaceToThePreflightIsTooLate(t *testing.T) {
	stubScheduleRestartWatch(t, nil)
	stubNotifyPrincipal(t)
	requestRestart(t)
	cancelRestartBeforeUpdate = func() {
		if v, err := runRestartPreflight("worker/kid"); err != nil || v.Verdict != "restart" {
			t.Errorf("racing preflight = %+v, %v; want restart", v, err)
		}
	}
	t.Cleanup(func() { cancelRestartBeforeUpdate = nil })

	text, isErr := callCancel(t, `{}`)
	if !isErr || !strings.Contains(text, "too late") {
		t.Fatalf("want too late after losing the race, got isErr=%v %q", isErr, text)
	}
}

func TestRearmAfterACancelIsRefused(t *testing.T) {
	consumedRestart(t)
	if _, isErr := callCancel(t, `{}`); !isErr {
		t.Fatal("setup: the cancel after the preflight must be too late")
	}

	v, err := runRestartRearm("worker/kid")
	if err != nil || v.Verdict != "none" || !strings.Contains(v.Reason, "cancel") {
		t.Fatalf("rearm after a cancel = %+v, %v; want none naming the cancel", v, err)
	}
}

// A restart that already came back is history, not in flight: cancelling
// then is the idempotent "nothing queued" success.
func TestCancelRestartAfterTheSessionCameBackIsNothingQueued(t *testing.T) {
	repoPath, _, _, _ := consumedRestart(t)
	st := readKid(t, repoPath)
	back := st.RestartingAt.Add(1)
	st.RestartedAt = &back
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}

	text, isErr := callCancel(t, `{}`)
	if isErr || !strings.Contains(strings.ToLower(text), "nothing") {
		t.Fatalf("want the nothing-queued success, got isErr=%v %q", isErr, text)
	}
}
