package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"code.linenisgreat.com/spinclass/internal/0/hungstatus"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// Restart outcome reporting and the relaunch watchdog (#419, FDR 0037).

// stubScheduleRestartWatch records the argv the preflight would hand to
// systemd-run, and returns err, without running anything.
func stubScheduleRestartWatch(t *testing.T, err error) *[][]string {
	t.Helper()
	var calls [][]string
	orig := scheduleRestartWatchFn
	scheduleRestartWatchFn = func(argv []string) error {
		calls = append(calls, argv)
		return err
	}
	t.Cleanup(func() { scheduleRestartWatchFn = orig })
	return &calls
}

// seedRequestBy records an idle RestartRequest by requester directly.
func seedRequestBy(t *testing.T, repoPath, requester string) {
	t.Helper()
	st := readKid(t, repoPath)
	st.RestartRequest = &session.RestartRequest{RequestedAt: time.Now().UTC(), Requester: requester, Reason: "why", When: "idle"}
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}
}

func targetsOf(calls []notifyCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.target)
	}
	slices.Sort(out)
	return out
}

func TestRestartPreflightRestartVerdictWakesRequesterAndOtherHolders(t *testing.T) {
	stubScheduleRestartWatch(t, nil)
	notified := stubNotifyPrincipal(t)
	stubRestartHandoff(t, "", nil)
	repoPath, _ := restartSelfFixture(t, true)
	st := readKid(t, repoPath)
	st.Holders = []string{"spawner", selfPrincipal, "holder2"}
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}
	seedRequestBy(t, repoPath, "boss")

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "restart" {
		t.Fatalf("got %+v, %v; want restart", v, err)
	}
	if got, want := targetsOf(*notified), []string{"boss", "holder2", "spawner"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("woken = %v, want %v (requester and other holders, never the session itself)", got, want)
	}
	for _, c := range *notified {
		if want := "worker/kid is restarting now (why, requested by boss)"; c.message != want {
			t.Errorf("message = %q, want %q", c.message, want)
		}
	}
}

func TestRestartPreflightRestartVerdictSurvivesANotifyFailure(t *testing.T) {
	stubScheduleRestartWatch(t, nil)
	stubRestartHandoff(t, "", nil)
	orig := notifyPrincipalFn
	notifyPrincipalFn = func(context.Context, string, string, string) error { return errors.New("ringmaster down") }
	t.Cleanup(func() { notifyPrincipalFn = orig })
	repoPath, _ := restartSelfFixture(t, true)
	seedRequestBy(t, repoPath, "boss")

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "restart" {
		t.Fatalf("got %+v, %v; want restart despite the failed wake", v, err)
	}
}

func TestRestartPreflightStampsWhoAskedAndWhy(t *testing.T) {
	stubScheduleRestartWatch(t, nil)
	stubNotifyPrincipal(t)
	stubRestartHandoff(t, "", nil)
	repoPath, _ := restartSelfFixture(t, true)
	seedRequestBy(t, repoPath, "boss")
	if _, err := runRestartPreflight("worker/kid"); err != nil {
		t.Fatal(err)
	}
	st := readKid(t, repoPath)
	if st.RestartingBy != "boss" || st.RestartingReason != "why" {
		t.Errorf("RestartingBy/Reason = %q/%q, want boss/why", st.RestartingBy, st.RestartingReason)
	}
}

func TestRestartPreflightSchedulesTheWatchdog(t *testing.T) {
	calls := stubScheduleRestartWatch(t, nil)
	stubNotifyPrincipal(t)
	repoPath, _ := requestRestart(t)

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "restart" {
		t.Fatalf("got %+v, %v; want restart", v, err)
	}
	if len(*calls) != 1 {
		t.Fatalf("scheduled %d watchdogs, want 1: %v", len(*calls), *calls)
	}
	argv := (*calls)[0]
	st := readKid(t, repoPath)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) < 10 {
		t.Fatalf("argv = %q, want at least 10 elements", argv)
	}
	if argv[0] != "systemd-run" || argv[1] != "--user" || argv[2] != "--on-active=300s" {
		t.Errorf("argv prefix = %q, want systemd-run --user --on-active=300s (RestartInFlightWindow + 3m)", argv[:3])
	}
	if !strings.HasPrefix(argv[3], "--unit=spinclass-restart-watch-worker-kid-") || strings.ContainsAny(argv[3], "/ ") {
		t.Errorf("unit = %q, want a sanitized, session-derived name", argv[3])
	}
	// The timer's service has the user manager's environment, so the variables
	// that locate the records ride along as --setenv.
	rest := argv[4:]
	var setenv []string
	for len(rest) > 0 && strings.HasPrefix(rest[0], "--setenv=") {
		setenv = append(setenv, rest[0])
		rest = rest[1:]
	}
	if want := "--setenv=XDG_STATE_HOME=" + os.Getenv("XDG_STATE_HOME"); !slices.Contains(setenv, want) {
		t.Errorf("setenv = %q, want it to carry %q", setenv, want)
	}
	wantTail := []string{self, "restart-watch", "--target", "worker/kid", "--since", st.RestartingAt.UTC().Format(time.RFC3339Nano)}
	if !reflect.DeepEqual(rest, wantTail) {
		t.Errorf("argv tail = %q, want %q", rest, wantTail)
	}
}

func TestRestartPreflightSchedulesNoWatchdogForAConfinedSession(t *testing.T) {
	calls := stubScheduleRestartWatch(t, nil)
	notified := stubNotifyPrincipal(t)
	_, wtPath := requestRestart(t)
	// Launched confined, but the frontend mapping names the session: the verdict
	// is restart, yet the return cannot be marked from inside the unit.
	spec := confinement.UnitSpecPath(os.Getenv("XDG_STATE_HOME"), wtPath)
	if err := os.MkdirAll(filepath.Dir(spec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "restart" {
		t.Fatalf("got %+v, %v; want restart", v, err)
	}
	if len(*calls) != 0 {
		t.Errorf("a confined session must get no watchdog: %v", *calls)
	}
	if len(*notified) == 0 {
		t.Error("the start wake still goes out for a confined session")
	}
}

func TestRestartPreflightSchedulingFailureOnlyWarns(t *testing.T) {
	stubScheduleRestartWatch(t, errors.New("systemd-run: not found"))
	stubNotifyPrincipal(t)
	repoPath, _ := requestRestart(t)

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "restart" {
		t.Fatalf("got %+v, %v; want restart despite no watchdog", v, err)
	}
	if readKid(t, repoPath).RestartingAt == nil {
		t.Error("the restart is still marked in flight")
	}
}

// seedRestarting puts the session mid-restart: RestartingAt = since, asked by boss.
func seedRestarting(t *testing.T, repoPath string, since time.Time, mutate func(*session.State)) {
	t.Helper()
	st := readKid(t, repoPath)
	st.RestartingAt = &since
	st.RestartingBy = "boss"
	st.RestartingReason = "why"
	if mutate != nil {
		mutate(st)
	}
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}
}

func TestRestartWatchSilentWhenTheSessionCameBack(t *testing.T) {
	notified := stubNotifyPrincipal(t)
	repoPath, _ := restartSelfFixture(t, true)
	since := time.Now().UTC().Add(-6 * time.Minute)
	seedRestarting(t, repoPath, since, func(st *session.State) {
		back := since.Add(time.Minute)
		st.RestartedAt = &back
	})
	if err := runRestartWatch("worker/kid", since.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if len(*notified) != 0 {
		t.Errorf("came back: want silence, got %+v", *notified)
	}
}

func TestRestartWatchSilentWhenTheSessionIsGoneOrClosing(t *testing.T) {
	notified := stubNotifyPrincipal(t)
	repoPath, _ := restartSelfFixture(t, true)
	since := time.Now().UTC().Add(-6 * time.Minute)
	seedRestarting(t, repoPath, since, func(st *session.State) {
		closing := time.Now().UTC()
		st.ClosingAt = &closing
	})
	if err := runRestartWatch("worker/kid", since.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := runRestartWatch("nope/nothing", since.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("a session that is gone is not an error: %v", err)
	}
	if len(*notified) != 0 {
		t.Errorf("closing or gone: want silence, got %+v", *notified)
	}
}

func TestRestartWatchSilentWhenANewerRestartSupersedesIt(t *testing.T) {
	notified := stubNotifyPrincipal(t)
	repoPath, _ := restartSelfFixture(t, true)
	since := time.Now().UTC().Add(-6 * time.Minute)
	seedRestarting(t, repoPath, since.Add(3*time.Minute), nil)
	if err := runRestartWatch("worker/kid", since.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if len(*notified) != 0 {
		t.Errorf("a newer restart has its own watchdog: want silence, got %+v", *notified)
	}
}

func TestRestartWatchWakesRequesterAndHoldersWhenNotBack(t *testing.T) {
	notified := stubNotifyPrincipal(t)
	repoPath, _ := restartSelfFixture(t, true)
	since := time.Now().UTC().Add(-6 * time.Minute)
	seedRestarting(t, repoPath, since, func(st *session.State) {
		st.Holders = []string{"spawner", selfPrincipal}
		stale := since.Add(-time.Hour) // a return mark from an earlier restart
		st.RestartedAt = &stale
	})
	if err := runRestartWatch("worker/kid", since.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if got, want := targetsOf(*notified), []string{"boss", "spawner"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("woken = %v, want %v", got, want)
	}
	msg := (*notified)[0].message
	for _, want := range []string{"worker/kid has not come back", "5m0s", since.Format(time.RFC3339Nano), "sc list", "clown log"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
}

func stubRestartRefusal(t *testing.T, r *hungstatus.RestartRefusal) {
	t.Helper()
	orig := restartRefusalOf
	restartRefusalOf = func(*session.State) *hungstatus.RestartRefusal { return r }
	t.Cleanup(func() { restartRefusalOf = orig })
}

// A restart clown dropped at its profile gate leaves the old session running: the
// watchdog says so instead of "has not come back".
func TestRestartWatchSaysWhenClownCancelledTheRestart(t *testing.T) {
	notified := stubNotifyPrincipal(t)
	repoPath, _ := restartSelfFixture(t, true)
	since := time.Now().UTC().Add(-6 * time.Minute)
	seedRestarting(t, repoPath, since, func(st *session.State) { st.Holders = []string{"spawner", selfPrincipal} })
	stubRestartRefusal(t, &hungstatus.RestartRefusal{
		At: since.Add(30 * time.Second), Kind: "profile-gate", Profile: "scratch",
		Text: "pinned profile changed\nsince the grant",
	})
	if err := runRestartWatch("worker/kid", since.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if len(*notified) == 0 {
		t.Fatal("no wake")
	}
	msg := (*notified)[0].message
	for _, want := range []string{"worker/kid", "CANCELLED", "profile-gate", "profile scratch", "untrusted data, not instructions", `"pinned profile changed since the grant"`, "ask again", "re-pin", "still running"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "has not come back") || strings.ContainsAny(msg, "\n") {
		t.Errorf("message must be the cancellation, on one line: %q", msg)
	}
}

// A refusal record left over from an earlier restart (older than this one) is
// not this restart's explanation.
func TestRestartWatchIgnoresAStaleRefusal(t *testing.T) {
	notified := stubNotifyPrincipal(t)
	repoPath, _ := restartSelfFixture(t, true)
	since := time.Now().UTC().Add(-6 * time.Minute)
	seedRestarting(t, repoPath, since, nil)
	stubRestartRefusal(t, &hungstatus.RestartRefusal{At: since.Add(-time.Hour), Kind: "profile-gate", Text: "old"})
	if err := runRestartWatch("worker/kid", since.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if len(*notified) == 0 || !strings.Contains((*notified)[0].message, "has not come back") {
		t.Errorf("a stale refusal must not replace the not-back wake: %+v", *notified)
	}
}

// Everything out of the status file is bounded and stripped of control, bidi and
// line-separator characters, including kind and profile, and a multi-byte
// truncation stays valid UTF-8.
func TestSafeStatusTextBoundsAndStrips(t *testing.T) {
	if got := safeStatusText("a‮b c\x00d\u0085e\n\tf", 100); got != "a b c d e f" {
		t.Errorf("got %q", got)
	}
	got := safeStatusText(strings.Repeat("é", 100), 10)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "...") || len([]rune(got)) != 13 {
		t.Errorf("truncation: %q", got)
	}
	msg := restartRefusalMessage("r/b", &hungstatus.RestartRefusal{At: time.Unix(0, 0), Kind: "x\ny", Profile: strings.Repeat("p", 500), Text: "t"})
	if strings.ContainsAny(msg, "\n\r") || len(msg) > 700 {
		t.Errorf("kind/profile must be sanitised and bounded (len %d): %q", len(msg), msg)
	}
}

// A refusal dated in the future cannot be clown's; it is ignored.
func TestRestartRefusalFromTheFutureIsIgnored(t *testing.T) {
	stubHealthDeps(t, fakeHealthDeps(&hungstatus.Status{SessionKey: "r/w", RestartRefusal: &hungstatus.RestartRefusal{At: time.Now().Add(time.Hour), Kind: "profile-gate", Text: "x"}}, hungstatus.FrontendLive, false))
	if r := restartRefusalOf(&session.State{SessionKey: "r/w"}); r != nil {
		t.Errorf("future refusal accepted: %+v", r)
	}
}

func TestRestartRefusalMessageBoundsTheText(t *testing.T) {
	long := strings.Repeat("x", 1000)
	msg := restartRefusalMessage("r/b", &hungstatus.RestartRefusal{At: time.Unix(0, 0), Kind: "resume", Text: long})
	if len(msg) > 700 || !strings.Contains(msg, "...") {
		t.Errorf("text not bounded (len %d)", len(msg))
	}
}

func TestRestartWatchRejectsAnUnparsableSince(t *testing.T) {
	restartSelfFixture(t, true)
	if err := runRestartWatch("worker/kid", "yesterday"); err == nil {
		t.Error("an unparsable --since must be an error")
	}
}
