package hungstatus

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

const stamp = "2006-01-02T15:04:05.000000000Z"

func writeStatus(t *testing.T, stateHome, principal, body string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(principal))
	dir := filepath.Join(stateHome, "clown", "status")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPathIsTheSha256OfThePrincipal(t *testing.T) {
	sum := sha256.Sum256([]byte("p-1"))
	want := filepath.Join("/state", "clown", "status", hex.EncodeToString(sum[:])+".json")
	if got := Path("/state", "p-1"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestReadParsesTheContractFields(t *testing.T) {
	state := t.TempDir()
	writeStatus(t, state, "p-1", fmt.Sprintf(`{"version":1,"principal":"p-1","spinclass_session_key":"r/b","turn_running":true,"turn_started_at":%q,"last_hook_at":%q,"frontend_pid":42,"frontend_start_ticks":777,"unit":"u.service","main_pid":9,"transcript_path":"/t/x.jsonl","written_at":%q}`,
		t0.Format(stamp), t0.Add(time.Minute).Format(stamp), t0.Add(2*time.Minute).Format(stamp)))
	s, err := Read(state, "p-1")
	if err != nil || s == nil {
		t.Fatalf("Read = %+v, %v", s, err)
	}
	if !s.TurnRunning || !s.TurnStartedAt.Equal(t0) || !s.LastHookAt.Equal(t0.Add(time.Minute)) || s.FrontendPID != 42 || s.FrontendStartTicks != 777 || s.SessionKey != "r/b" || s.TranscriptPath != "/t/x.jsonl" {
		t.Fatalf("parsed %+v", s)
	}
}

// clown 66e0642f4a9c: an optional, frontend-written restart_refusal record; the
// status version stays 1 and the field is omitted when there is none.
func TestReadParsesTheRestartRefusal(t *testing.T) {
	state := t.TempDir()
	writeStatus(t, state, "p", fmt.Sprintf(`{"version":1,"restart_refusal":{"at":%q,"kind":"profile-gate","profile":"scratch","pinned_digest":"","current_digest":"","text":"the pinned profile changed"}}`, t0.Format(stamp)))
	s, err := Read(state, "p")
	if err != nil || s == nil || s.RestartRefusal == nil {
		t.Fatalf("got %+v, %v", s, err)
	}
	r := s.RestartRefusal
	if !r.At.Equal(t0) || r.Kind != "profile-gate" || r.Profile != "scratch" || r.Text != "the pinned profile changed" {
		t.Errorf("refusal = %+v", r)
	}
	writeStatus(t, state, "q", `{"version":1}`)
	if s, _ := Read(state, "q"); s == nil || s.RestartRefusal != nil {
		t.Errorf("no restart_refusal must read as nil, got %+v", s)
	}
}

func TestReadAbsentIsNilNotAnError(t *testing.T) {
	if s, err := Read(t.TempDir(), "nobody"); s != nil || err != nil {
		t.Fatalf("got %+v, %v; want nil, nil", s, err)
	}
}

func TestReadEmptyTimesAreZero(t *testing.T) {
	state := t.TempDir()
	writeStatus(t, state, "p", `{"version":1,"turn_running":false,"turn_started_at":"","last_hook_at":""}`)
	s, err := Read(state, "p")
	if err != nil || s == nil || !s.TurnStartedAt.IsZero() || !s.LastHookAt.IsZero() {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func TestReadRefusesASymlinkAndANonRegularFile(t *testing.T) {
	state := t.TempDir()
	p := writeStatus(t, state, "p", `{"version":1}`)
	real := p + ".real"
	if err := os.Rename(p, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, p); err != nil {
		t.Fatal(err)
	}
	if s, err := Read(state, "p"); s != nil || err == nil {
		t.Fatalf("symlinked status: got %+v, %v; want an error", s, err)
	}
}

func TestReadRefusesAnUnknownVersion(t *testing.T) {
	state := t.TempDir()
	writeStatus(t, state, "p", `{"version":2}`)
	if s, err := Read(state, "p"); s != nil || err == nil {
		t.Fatalf("got %+v, %v; want an error for version 2", s, err)
	}
}

func fakeProc(t *testing.T, pid int, ticks string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// field 22 is starttime; fields 3.. follow "(comm) ".
	stat := fmt.Sprintf("%d (cl own) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 %s 0 0", pid, ticks)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFrontendAliveComparesPidAndStartTicks(t *testing.T) {
	root := fakeProc(t, 42, "777")
	if st := FrontendState(root, 42, 777); st != FrontendLive {
		t.Errorf("same pid and ticks: %v, want live", st)
	}
	if st := FrontendState(root, 42, 778); st != FrontendDead {
		t.Errorf("reused pid (ticks differ): %v, want dead", st)
	}
	if st := FrontendState(root, 43, 777); st != FrontendDead {
		t.Errorf("no such pid: %v, want dead", st)
	}
	if st := FrontendState(root, 42, 0); st != FrontendLive {
		t.Errorf("unknown ticks but pid present: %v, want live", st)
	}
	if st := FrontendState(root, 0, 0); st != FrontendUnknown {
		t.Errorf("no pid recorded: %v, want unknown", st)
	}
}

func busy(startedAgo, hookAgo time.Duration) *Status {
	return &Status{TurnRunning: true, TurnStartedAt: t0.Add(-startedAgo), LastHookAt: t0.Add(-hookAgo), FrontendPID: 42}
}

func TestClassify(t *testing.T) {
	bound := 60 * time.Minute
	for name, tc := range map[string]struct {
		in   Input
		want Verdict
	}{
		"no file is unknown, never healthy": {Input{Now: t0, Bound: bound}, VerdictUnknown},
		"idle is ok":                        {Input{Status: &Status{TurnRunning: false, TurnStartedAt: t0.Add(-5 * time.Hour)}, Frontend: FrontendLive, Now: t0, Bound: bound}, VerdictOK},
		"busy under the bound is ok":        {Input{Status: busy(10*time.Minute, 10*time.Minute), Frontend: FrontendLive, Now: t0, Bound: bound}, VerdictOK},
		"busy over the bound is too long":   {Input{Status: busy(2*time.Hour, 2*time.Hour), Frontend: FrontendLive, Now: t0, Bound: bound}, VerdictBusyTooLong},
		"busy over the bound but a recent hook is ok": {
			Input{Status: busy(2*time.Hour, 5*time.Minute), Frontend: FrontendLive, Now: t0, Bound: bound}, VerdictOK,
		},
		"busy over the bound but the transcript moved is ok": {
			Input{Status: busy(2*time.Hour, 2*time.Hour), Frontend: FrontendLive, TranscriptMTime: t0.Add(-time.Minute), Now: t0, Bound: bound}, VerdictOK,
		},
		"a running merge or check job excuses a busy turn": {
			Input{Status: busy(2*time.Hour, 2*time.Hour), Frontend: FrontendLive, JobRunning: true, Now: t0, Bound: bound}, VerdictOK,
		},
		"a dead frontend is dead, not hung": {
			Input{Status: busy(2*time.Hour, 2*time.Hour), Frontend: FrontendDead, Now: t0, Bound: bound}, VerdictFrontendDead,
		},
		"an unknown frontend with an over-bound turn is still too long": {
			Input{Status: busy(2*time.Hour, 2*time.Hour), Frontend: FrontendUnknown, Now: t0, Bound: bound}, VerdictBusyTooLong,
		},
		"a busy turn with no start time is unknown": {
			Input{Status: &Status{TurnRunning: true}, Frontend: FrontendLive, Now: t0, Bound: bound}, VerdictUnknown,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got, why := Classify(tc.in); got != tc.want {
				t.Errorf("Classify = %v (%s), want %v", got, why, tc.want)
			}
		})
	}
}

func TestTranscriptMTimeNeverFollowsOrReadsNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := t0.Add(-time.Hour)
	if err := os.Chtimes(f, want, want); err != nil {
		t.Fatal(err)
	}
	if got := TranscriptMTime(f); !got.Equal(want) {
		t.Errorf("regular file: %v, want %v", got, want)
	}
	link := filepath.Join(dir, "l")
	if err := os.Symlink(f, link); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"symlink": link, "directory": dir, "relative": "t.jsonl", "empty": "", "missing": filepath.Join(dir, "none")} {
		if got := TranscriptMTime(p); !got.IsZero() {
			t.Errorf("%s: %v, want zero", name, got)
		}
	}
}
