package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// Starting a stopped session's agent again
// (docs/plans/2026-11-20-stop-start-rights.md): strictly by the start right,
// through the trusted [session-entry].start-handoff, which resumes the same
// principal with one required first message.

const startHandoffTOML = `start-handoff = ["env", "CLOWN_SESSION_ID={principal}", "clown", "--clown-attach=spawn", "--provider", "claude", "--", "--resume", "{principal}", "{message}"]` + "\n"

func callStartChild(t *testing.T, args string) (text string, isErr bool) {
	t.Helper()
	res, err := handleStartChildSession(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("handleStartChildSession: %v", err)
	}
	return res.Text, res.IsErr
}

// stoppedChildFixture stages worker/kid stopped by "stopper", held by caller
// with rights, and the start hand-off configured when handoff is set.
func stoppedChildFixture(t *testing.T, caller, rights string, handoff bool) (repoPath, wtPath string) {
	t.Helper()
	stoppedAt := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	repoPath, wtPath = restartChildFixture(t, caller, session.State{
		Holders:       []string{caller},
		HandleRights:  map[string]string{caller: rights},
		StoppedAt:     &stoppedAt,
		StoppedBy:     "stopper",
		StoppedReason: "quiet hours",
	})
	if handoff {
		writeGlobalSweatfile(t, os.Getenv("HOME"), restartHandoffTOML+startHandoffTOML)
	}
	return repoPath, wtPath
}

func TestStartChildNeedsTheStartRight(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := stoppedChildFixture(t, "holder", "observe,restart,stop", true)

	text, isErr := callStartChild(t, `{"child":"worker/kid","message":"carry on"}`)
	if !isErr || !strings.Contains(text, "start-child-session needs start") {
		t.Fatalf("want a refusal naming the start right, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).StoppedAt == nil || len(*calls) != 0 {
		t.Error("a refused start must change and run nothing")
	}
}

func TestStartChildRequiresAMessage(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stoppedChildFixture(t, "holder", "observe,start", true)

	text, isErr := callStartChild(t, `{"child":"worker/kid","message":"  "}`)
	if !isErr || !strings.Contains(text, "message") {
		t.Fatalf("want a refusal naming the message, got isErr=%v %q", isErr, text)
	}
	if len(*calls) != 0 {
		t.Error("no hand-off may run without a message")
	}
}

func TestStartChildRefusesASessionThatIsNotStopped(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	restartChildFixture(t, "holder", session.State{
		Holders:      []string{"holder"},
		HandleRights: map[string]string{"holder": "observe,start"},
	})
	writeGlobalSweatfile(t, os.Getenv("HOME"), restartHandoffTOML+startHandoffTOML)

	text, isErr := callStartChild(t, `{"child":"worker/kid","message":"carry on"}`)
	if !isErr || !strings.Contains(text, "not stopped") {
		t.Fatalf("want a refusal saying it is not stopped, got isErr=%v %q", isErr, text)
	}
	if len(*calls) != 0 {
		t.Error("no hand-off may run")
	}
}

func TestStartChildRefusesWithoutAHandoff(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stoppedChildFixture(t, "holder", "observe,start", false)

	text, isErr := callStartChild(t, `{"child":"worker/kid","message":"carry on"}`)
	if !isErr || !strings.Contains(text, "start-handoff") {
		t.Fatalf("want a refusal naming [session-entry] start-handoff, got isErr=%v %q", isErr, text)
	}
	if len(*calls) != 0 {
		t.Error("no hand-off may run")
	}
}

func TestStartChildRunsTheHandoffWithTheNoticeAndClearsTheStop(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, wtPath := stoppedChildFixture(t, "holder", "observe,start", true)

	text, isErr := callStartChild(t, `{"child":"worker/kid","message":"your merge landed; carry on"}`)
	if isErr {
		t.Fatalf("a holder with start must be allowed: %q", text)
	}
	if len(*calls) != 1 {
		t.Fatalf("want one hand-off, got %+v", *calls)
	}
	call := (*calls)[0]
	if call.dir != wtPath {
		t.Errorf("the hand-off runs in the child's worktree: %q", call.dir)
	}
	argv := call.argv
	if argv[1] != "CLOWN_SESSION_ID="+childPrincipal || argv[len(argv)-2] != childPrincipal {
		t.Errorf("the principal must be the child's own: %q", argv)
	}
	msg := argv[len(argv)-1]
	for _, want := range []string{"stopped at 2026-11-20T09:00:00Z", "by stopper", "quiet hours", "started again", "by holder", "your merge landed; carry on"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the first message %q is missing %q", msg, want)
		}
	}
	st := readKid(t, repoPath)
	if st.StoppedAt != nil || st.StoppedBy != "" || st.StoppedReason != "" {
		t.Errorf("a started session is no longer stopped: %v %q %q", st.StoppedAt, st.StoppedBy, st.StoppedReason)
	}
}

func TestStartChildKeepsTheStopWhenTheHandoffFails(t *testing.T) {
	stubRestartHandoff(t, "boom", os.ErrPermission)
	repoPath, _ := stoppedChildFixture(t, "holder", "observe,start", true)

	text, isErr := callStartChild(t, `{"child":"worker/kid","message":"carry on"}`)
	if !isErr || !strings.Contains(text, "boom") {
		t.Fatalf("want the hand-off failure reported, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).StoppedAt == nil {
		t.Error("a failed start leaves the session stopped")
	}
}
