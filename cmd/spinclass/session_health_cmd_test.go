package main

import (
	"errors"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/hungstatus"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

var healthNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func fakeHealthDeps(status *hungstatus.Status, frontend hungstatus.Frontend, jobRunning bool) healthDeps {
	return healthDeps{
		Now:        healthNow,
		Bound:      func(*session.State) time.Duration { return 60 * time.Minute },
		Principal:  func(*session.State) (string, error) { return "p-1", nil },
		ReadStatus: func(string) (*hungstatus.Status, error) { return status, nil },
		Frontend:   func(*hungstatus.Status) hungstatus.Frontend { return frontend },
		Transcript: func(string) time.Time { return time.Time{} },
		JobRunning: func(*session.State) bool { return jobRunning },
	}
}

func busyStatus(key string, ago time.Duration) *hungstatus.Status {
	return &hungstatus.Status{SessionKey: key, TurnRunning: true, TurnStartedAt: healthNow.Add(-ago), LastHookAt: healthNow.Add(-ago)}
}

func TestSessionHealthVerdicts(t *testing.T) {
	st := &session.State{SessionKey: "r/w", WorktreePath: "/wt/r/w"}
	for name, tc := range map[string]struct {
		deps healthDeps
		want hungstatus.Verdict
	}{
		"busy two hours":             {fakeHealthDeps(busyStatus("r/w", 2*time.Hour), hungstatus.FrontendLive, false), hungstatus.VerdictBusyTooLong},
		"busy two hours with a job":  {fakeHealthDeps(busyStatus("r/w", 2*time.Hour), hungstatus.FrontendLive, true), hungstatus.VerdictOK},
		"idle":                       {fakeHealthDeps(&hungstatus.Status{SessionKey: "r/w"}, hungstatus.FrontendLive, false), hungstatus.VerdictOK},
		"no file":                    {fakeHealthDeps(nil, hungstatus.FrontendUnknown, false), hungstatus.VerdictUnknown},
		"dead frontend":              {fakeHealthDeps(busyStatus("r/w", 2*time.Hour), hungstatus.FrontendDead, false), hungstatus.VerdictFrontendDead},
		"file names another session": {fakeHealthDeps(busyStatus("other/w", 2*time.Hour), hungstatus.FrontendLive, false), hungstatus.VerdictUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			if got, why := sessionHealth(st, tc.deps); got != tc.want {
				t.Errorf("verdict = %v (%s), want %v", got, why, tc.want)
			}
		})
	}
}

func TestSessionHealthUnreadableInputsAreUnknownNeverHealthy(t *testing.T) {
	st := &session.State{SessionKey: "r/w"}
	d := fakeHealthDeps(nil, hungstatus.FrontendUnknown, false)
	d.Principal = func(*session.State) (string, error) { return "", errors.New("no principal") }
	if got, _ := sessionHealth(st, d); got != hungstatus.VerdictUnknown {
		t.Errorf("unresolved principal: %v, want unknown", got)
	}
	d = fakeHealthDeps(nil, hungstatus.FrontendUnknown, false)
	d.ReadStatus = func(string) (*hungstatus.Status, error) { return nil, errors.New("not a regular file") }
	if got, _ := sessionHealth(st, d); got != hungstatus.VerdictUnknown {
		t.Errorf("unreadable status: %v, want unknown", got)
	}
}
