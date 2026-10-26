package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/hungstatus"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

func stubHealthDeps(t *testing.T, d healthDeps) {
	t.Helper()
	orig := healthDepsFn
	healthDepsFn = func() healthDeps { return d }
	t.Cleanup(func() { healthDepsFn = orig })
}

func callRestartHung(t *testing.T, args string) (string, bool) {
	t.Helper()
	res, err := handleRestartHungChild(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("handleRestartHungChild: %v", err)
	}
	return res.Text, res.IsErr
}

func hungFixture(t *testing.T, rights string) (repoPath string) {
	t.Helper()
	repoPath, _ = restartChildFixture(t, "holder", session.State{
		Holders: []string{"holder"}, HandleRights: map[string]string{"holder": rights},
	})
	return repoPath
}

func hungDeps(status *hungstatus.Status, jobRunning bool) healthDeps {
	d := fakeHealthDeps(status, hungstatus.FrontendLive, jobRunning)
	d.Principal = func(*session.State) (string, error) { return childPrincipal, nil }
	return d
}

func TestRestartHungChildForcesARestartOfABusyTooLongChild(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	wakes := stubNotifyPrincipal(t)
	repoPath := hungFixture(t, "observe,restart")
	stubHealthDeps(t, hungDeps(busyStatus("worker/kid", 2*time.Hour), false))

	text, isErr := callRestartHung(t, `{"child":"worker/kid"}`)
	if isErr {
		t.Fatalf("a hung child with the restart right must be restarted: %q", text)
	}
	if len(*calls) != 1 || !containsArg((*calls)[0].argv, "now") {
		t.Fatalf("hand-off calls = %+v, want one forced (--when now)", *calls)
	}
	kid := readKid(t, repoPath)
	if kid.ForcedRestartAt == nil || !kid.ForcedRestartAt.Equal(healthNow) {
		t.Errorf("ForcedRestartAt = %v, want %v", kid.ForcedRestartAt, healthNow)
	}
	if kid.RestartRequest == nil || kid.RestartRequest.When != "now" || !strings.HasPrefix(kid.RestartRequest.Reason, "hung:") {
		t.Errorf("restart request = %+v, want a forced one whose reason starts with hung:", kid.RestartRequest)
	}
	if len(*wakes) == 0 || !strings.Contains((*wakes)[0].message, "force-restarted as hung") {
		t.Errorf("holders must be woken, got %+v", *wakes)
	}
}

func containsArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func TestRestartHungChildRefusesAChildThatIsNotHung(t *testing.T) {
	for name, d := range map[string]healthDeps{
		"busy within the bound": hungDeps(busyStatus("worker/kid", 10*time.Minute), false),
		"busy but a job runs":   hungDeps(busyStatus("worker/kid", 2*time.Hour), true),
		"idle":                  hungDeps(&hungstatus.Status{SessionKey: "worker/kid"}, false),
		"no status file":        hungDeps(nil, false),
	} {
		t.Run(name, func(t *testing.T) {
			calls := stubRestartHandoff(t, "", nil)
			repoPath := hungFixture(t, "observe,restart")
			stubHealthDeps(t, d)
			text, isErr := callRestartHung(t, `{"child":"worker/kid"}`)
			if !isErr || !strings.Contains(text, "not hung") {
				t.Fatalf("want a not-hung refusal, got isErr=%v %q", isErr, text)
			}
			if len(*calls) != 0 || readKid(t, repoPath).ForcedRestartAt != nil {
				t.Error("a child that is not hung must neither be restarted nor spend its day")
			}
		})
	}
}

func TestRestartHungChildNeedsTheRestartRight(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath := hungFixture(t, "observe,close")
	stubHealthDeps(t, hungDeps(busyStatus("worker/kid", 2*time.Hour), false))
	text, isErr := callRestartHung(t, `{"child":"worker/kid"}`)
	if !isErr || !strings.Contains(text, "restart") {
		t.Fatalf("want a missing-right refusal, got isErr=%v %q", isErr, text)
	}
	if len(*calls) != 0 || readKid(t, repoPath).ForcedRestartAt != nil {
		t.Error("no right: nothing may happen")
	}
}

func TestRestartHungChildIsBoundedToOncePerDay(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stubNotifyPrincipal(t)
	hungFixture(t, "observe,restart")
	d := hungDeps(busyStatus("worker/kid", 2*time.Hour), false)
	stubHealthDeps(t, d)
	if text, isErr := callRestartHung(t, `{"child":"worker/kid"}`); isErr {
		t.Fatalf("first use: %q", text)
	}
	text, isErr := callRestartHung(t, `{"child":"worker/kid"}`)
	if !isErr || !strings.Contains(text, "at most once") {
		t.Fatalf("second use within a day must be refused, got isErr=%v %q", isErr, text)
	}
	if len(*calls) != 1 {
		t.Errorf("hand-off calls = %d, want 1", len(*calls))
	}
	// A day later it may be used again.
	later := d
	later.Now = d.Now.Add(hungRestartCooldown + time.Minute)
	later.ReadStatus = func(string) (*hungstatus.Status, error) {
		return busyStatus("worker/kid", 2*time.Hour+hungRestartCooldown+time.Minute), nil
	}
	stubHealthDeps(t, later)
	if text, isErr := callRestartHung(t, `{"child":"worker/kid"}`); isErr {
		t.Fatalf("after the cooldown: %q", text)
	}
}

func TestRestartHungChildHandoffFailureDoesNotSpendTheDay(t *testing.T) {
	stubRestartHandoff(t, "boom", errors.New("exit status 1"))
	repoPath := hungFixture(t, "observe,restart")
	stubHealthDeps(t, hungDeps(busyStatus("worker/kid", 2*time.Hour), false))
	if _, isErr := callRestartHung(t, `{"child":"worker/kid"}`); !isErr {
		t.Fatal("a failed hand-off must be an error")
	}
	if readKid(t, repoPath).ForcedRestartAt != nil {
		t.Error("a failed hand-off must not spend the day")
	}
}

func TestRestartHungChildRefusesASessionNamingItself(t *testing.T) {
	kid := &session.State{SessionKey: "worker/kid"}
	if err := refuseSelfRestartAsHung("p-kid", "", "p-kid", kid); err == nil {
		t.Error("the same principal must be refused")
	}
	if err := refuseSelfRestartAsHung("p-other", "worker/kid", "p-kid", kid); err == nil {
		t.Error("the same session key must be refused")
	}
	if err := refuseSelfRestartAsHung("p-boss", "boss/main", "p-kid", kid); err != nil {
		t.Errorf("a different caller must pass: %v", err)
	}
}

func TestRestartHungChildRefusesAConfinedCaller(t *testing.T) {
	asConfinedServeCaller(t, "a-confined-principal")
	calls := stubRestartHandoff(t, "", nil)
	text, isErr := callRestartHung(t, `{"child":"worker/kid"}`)
	if !isErr || !strings.Contains(text, "classifies as confined") || len(*calls) != 0 {
		t.Fatalf("got isErr=%v %q calls=%d", isErr, text, len(*calls))
	}
}
