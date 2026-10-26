package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/hungstatus"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
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

func TestSessionHealthShowsAClownCancelledRestart(t *testing.T) {
	st := &session.State{SessionKey: "r/w"}
	status := &hungstatus.Status{SessionKey: "r/w", RestartRefusal: &hungstatus.RestartRefusal{
		At: healthNow.Add(-time.Minute), Kind: "profile-gate", Text: "pinned profile changed\x00",
	}}
	_, why := sessionHealth(st, fakeHealthDeps(status, hungstatus.FrontendLive, false))
	for _, want := range []string{"last restart cancelled by clown (profile-gate)", "pinned profile changed"} {
		if !strings.Contains(why, want) {
			t.Errorf("why %q lacks %q", why, want)
		}
	}
	if strings.ContainsRune(why, 0) {
		t.Error("control characters must be stripped")
	}
}

func TestUnitNote(t *testing.T) {
	start := healthNow.Add(-2 * 24 * time.Hour)
	for name, tc := range map[string]struct {
		clock  confinement.UnitClock
		ok     bool
		now    time.Time
		want   []string
		absent []string
	}{
		"not confined":        {ok: false, now: healthNow},
		"fresh":               {clock: confinement.UnitClock{Started: start}, ok: true, now: healthNow, want: []string{"unit age 48h0m0s", "left of 7d"}, absent: []string{"SOON", "EXITED", "PAST"}},
		"under 36 hours left": {clock: confinement.UnitClock{Started: start}, ok: true, now: start.Add(6*24*time.Hour - time.Hour), want: []string{"UNIT EXPIRES SOON", "restart-child-session now"}},
		"exactly 36h is fine": {clock: confinement.UnitClock{Started: start}, ok: true, now: start.Add(confinement.UnitWallClock - unitExpirySoon), want: []string{"left of 7d"}, absent: []string{"SOON"}},
		"past the clock":      {clock: confinement.UnitClock{Started: start}, ok: true, now: start.Add(8 * 24 * time.Hour), want: []string{"UNIT PAST"}},
		"exited":              {clock: confinement.UnitClock{Started: start, Exited: true, ExitCode: 1}, ok: true, now: healthNow, want: []string{"UNIT EXITED (code 1", "sc resume"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := unitNote(tc.clock, tc.ok, tc.now)
			if !tc.ok && got != "" {
				t.Fatalf("unconfined session got a note: %q", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("note %q lacks %q", got, w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("note %q must not contain %q", got, a)
				}
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
