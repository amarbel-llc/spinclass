package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/hungstatus"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// `sc stop --hard` stops a session whose frontend cannot honour a stop request
// (one older than the stop verdict): it records the stopped mark, then sends
// SIGTERM to the frontend pid clown's own status file names for the session's
// principal, after checking that pid is the live clown the file describes. It
// never scans /proc for a pid.

const (
	hardFrontendPID   = 4242
	hardFrontendTicks = uint64(987654)
)

// hardStopEnv stubs the hard stop's seams: a fake proc root and state home,
// the signal recorder, and the job probe. It returns the recorded signals.
func hardStopEnv(t *testing.T, jobRunning bool) (procRoot, stateHome string, signalled *[]int) {
	t.Helper()
	procRoot, stateHome = t.TempDir(), t.TempDir()
	var sent []int
	oldProc, oldState, oldSig, oldJob := hardStopProcRoot, hardStopStateHome, hardStopSignal, hardStopJobRunning
	t.Cleanup(func() {
		hardStopProcRoot, hardStopStateHome, hardStopSignal, hardStopJobRunning = oldProc, oldState, oldSig, oldJob
	})
	hardStopProcRoot = procRoot
	hardStopStateHome = func() string { return stateHome }
	hardStopSignal = func(pid int) error { sent = append(sent, pid); return nil }
	hardStopJobRunning = func(*session.State) bool { return jobRunning }
	stubNotifyPrincipal(t) // a hard stop wakes the holders
	return procRoot, stateHome, &sent
}

// fakeFrontendProc writes /proc/<pid>/stat with the given start ticks (field
// 22) and a /proc/<pid>/exe link to a binary named comm.
func fakeFrontendProc(t *testing.T, procRoot string, pid int, ticks uint64, comm string) {
	t.Helper()
	dir := filepath.Join(procRoot, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fields := []string{"S"}
	for i := 4; i <= 21; i++ {
		fields = append(fields, "0")
	}
	fields = append(fields, fmt.Sprint(ticks))
	stat := fmt.Sprintf("%d (%s) %s\n", pid, comm, strings.Join(fields, " "))
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("/nix/store/fake-frontend", "bin", comm), filepath.Join(dir, "exe")); err != nil {
		t.Fatal(err)
	}
}

// fakeStatusFile writes clown's status file for principal.
func fakeStatusFile(t *testing.T, stateHome, principal string, pid int, ticks uint64) {
	t.Helper()
	path := hungstatus.Path(stateHome, principal)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"version": 1, "principal": principal, "spinclass_session_key": "worker/kid",
		"frontend_pid": pid, "frontend_start_ticks": ticks,
	})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHardStopMarksStoppedThenSignalsTheFrontend(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, stderr := stubOperatorRestart(t, true, "y\n")
	repoPath := operatorStopFixture(t)
	procRoot, stateHome, _ := hardStopEnv(t, false)
	fakeFrontendProc(t, procRoot, hardFrontendPID, hardFrontendTicks, "clown")
	fakeStatusFile(t, stateHome, childPrincipal, hardFrontendPID, hardFrontendTicks)

	var markedBeforeSignal bool
	var sent []int
	hardStopSignal = func(pid int) error {
		markedBeforeSignal = readKid(t, repoPath).StoppedAt != nil
		sent = append(sent, pid)
		return nil
	}

	if err := runOperatorStop(operatorStopParams{Target: []string{"worker/kid"}, Hard: true, Reason: "old frontend"}); err != nil {
		t.Fatalf("err = %v (stdout %q)", err, stdout.String())
	}
	if len(sent) != 1 || sent[0] != hardFrontendPID {
		t.Fatalf("signalled %v, want only the status file's frontend pid %d", sent, hardFrontendPID)
	}
	if !markedBeforeSignal {
		t.Error("the stopped mark must be recorded before the frontend is signalled")
	}
	st := readKid(t, repoPath)
	if st.StoppedAt == nil || st.StoppedBy != operatorRestartRequester || st.StoppedReason != "old frontend" {
		t.Errorf("stopped mark = %v %q %q", st.StoppedAt, st.StoppedBy, st.StoppedReason)
	}
	if st.RestartRequest != nil || len(*calls) != 0 {
		t.Errorf("a hard stop queues no request and runs no hand-off: %+v %+v", st.RestartRequest, *calls)
	}
	if !strings.Contains(stderr.String(), "SIGTERM") || !strings.Contains(stderr.String(), "frontend") {
		t.Errorf("stderr = %q, want the prompt to say it kills the frontend", stderr.String())
	}
	if got, want := stdout.String(), "worker/kid: hard stop: frontend signalled\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// Every reason the pid cannot be trusted is refused with a clear line, before
// the prompt, with nothing marked and nothing signalled.
func TestHardStopRefusesAPidItCannotTrust(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, procRoot, stateHome string)
		want  string
	}{
		{"no status file", func(t *testing.T, procRoot, _ string) {
			fakeFrontendProc(t, procRoot, hardFrontendPID, hardFrontendTicks, "clown")
		}, "no clown status file"},
		{"no pid recorded", func(t *testing.T, _, stateHome string) {
			fakeStatusFile(t, stateHome, childPrincipal, 0, hardFrontendTicks)
		}, "names no frontend pid"},
		{"no start ticks", func(t *testing.T, procRoot, stateHome string) {
			fakeFrontendProc(t, procRoot, hardFrontendPID, hardFrontendTicks, "clown")
			fakeStatusFile(t, stateHome, childPrincipal, hardFrontendPID, 0)
		}, "start time"},
		{"stale pid", func(t *testing.T, procRoot, stateHome string) {
			fakeFrontendProc(t, procRoot, hardFrontendPID, hardFrontendTicks+1, "clown")
			fakeStatusFile(t, stateHome, childPrincipal, hardFrontendPID, hardFrontendTicks)
		}, "not alive or was reused"},
		{"dead pid", func(t *testing.T, _, stateHome string) {
			fakeStatusFile(t, stateHome, childPrincipal, hardFrontendPID, hardFrontendTicks)
		}, "not alive or was reused"},
		{"not a clown", func(t *testing.T, procRoot, stateHome string) {
			fakeFrontendProc(t, procRoot, hardFrontendPID, hardFrontendTicks, "bash")
			fakeStatusFile(t, stateHome, childPrincipal, hardFrontendPID, hardFrontendTicks)
		}, "not a clown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubRestartHandoff(t, "", nil)
			stdout, stderr := stubOperatorRestart(t, true, "y\n")
			repoPath := operatorStopFixture(t)
			procRoot, stateHome, sent := hardStopEnv(t, false)
			tc.setup(t, procRoot, stateHome)

			if err := runOperatorStop(operatorStopParams{Target: []string{"worker/kid"}, Hard: true}); err == nil {
				t.Fatal("want a nonzero exit")
			}
			if !strings.Contains(stdout.String(), "worker/kid: refused: ") || !strings.Contains(stdout.String(), tc.want) {
				t.Errorf("stdout = %q, want a refusal naming %q", stdout.String(), tc.want)
			}
			if strings.Contains(stderr.String(), "[y/N]") {
				t.Errorf("stderr = %q; a refused target must not be prompted for", stderr.String())
			}
			if len(*sent) != 0 || readKid(t, repoPath).StoppedAt != nil {
				t.Errorf("signalled %v, stopped %v; nothing may happen", *sent, readKid(t, repoPath).StoppedAt)
			}
		})
	}
}

// A status file naming another principal or another session is refused: it is
// not this session's.
func TestHardStopRefusesAStatusFileForAnotherSession(t *testing.T) {
	for _, tc := range []struct {
		principal, key, want string
	}{
		{"someone-else", "worker/kid", "names principal"},
		{childPrincipal, "worker/other", "names session"},
	} {
		stubRestartHandoff(t, "", nil)
		stdout, _ := stubOperatorRestart(t, true, "y\n")
		operatorStopFixture(t)
		procRoot, stateHome, sent := hardStopEnv(t, false)
		fakeFrontendProc(t, procRoot, hardFrontendPID, hardFrontendTicks, "clown")
		fakeStatusFile(t, stateHome, childPrincipal, hardFrontendPID, hardFrontendTicks)
		// Rewrite the file under the child's principal path, naming someone else.
		raw, _ := json.Marshal(map[string]any{
			"version": 1, "principal": tc.principal, "spinclass_session_key": tc.key,
			"frontend_pid": hardFrontendPID, "frontend_start_ticks": hardFrontendTicks,
		})
		if err := os.WriteFile(hungstatus.Path(stateHome, childPrincipal), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runOperatorStop(operatorStopParams{Target: []string{"worker/kid"}, Hard: true}); err == nil {
			t.Fatalf("%s: want a nonzero exit", tc.want)
		}
		if !strings.Contains(stdout.String(), tc.want) || len(*sent) != 0 {
			t.Errorf("%s: stdout %q signalled %v", tc.want, stdout.String(), *sent)
		}
	}
}

// A running merge or check job refuses a hard stop unless --now is also given.
func TestHardStopRefusesARunningJobUnlessNow(t *testing.T) {
	stubRestartHandoff(t, "", nil)
	stdout, _ := stubOperatorRestart(t, true, "y\n")
	operatorStopFixture(t)
	procRoot, stateHome, sent := hardStopEnv(t, true)
	fakeFrontendProc(t, procRoot, hardFrontendPID, hardFrontendTicks, "clown")
	fakeStatusFile(t, stateHome, childPrincipal, hardFrontendPID, hardFrontendTicks)

	if err := runOperatorStop(operatorStopParams{Target: []string{"worker/kid"}, Hard: true}); err == nil {
		t.Fatal("want a nonzero exit while a job runs")
	}
	if !strings.Contains(stdout.String(), "merge or check job") || len(*sent) != 0 {
		t.Errorf("stdout %q signalled %v", stdout.String(), *sent)
	}

	stdout, _ = stubOperatorRestart(t, true, "y\n")
	if err := runOperatorStop(operatorStopParams{Target: []string{"worker/kid"}, Hard: true, Now: true}); err != nil {
		t.Fatalf("--hard --now: %v (stdout %q)", err, stdout.String())
	}
	if len(*sent) != 1 {
		t.Errorf("signalled %v, want one with --now", *sent)
	}
}

// A signal that fails takes the stopped mark back, and the target is refused.
func TestHardStopUndoesTheMarkWhenTheSignalFails(t *testing.T) {
	stubRestartHandoff(t, "", nil)
	stdout, _ := stubOperatorRestart(t, true, "y\n")
	repoPath := operatorStopFixture(t)
	procRoot, stateHome, _ := hardStopEnv(t, false)
	fakeFrontendProc(t, procRoot, hardFrontendPID, hardFrontendTicks, "clown")
	fakeStatusFile(t, stateHome, childPrincipal, hardFrontendPID, hardFrontendTicks)
	hardStopSignal = func(int) error { return errors.New("operation not permitted") }

	if err := runOperatorStop(operatorStopParams{Target: []string{"worker/kid"}, Hard: true}); err == nil {
		t.Fatal("want a nonzero exit")
	}
	if !strings.Contains(stdout.String(), "worker/kid: refused: ") || !strings.Contains(stdout.String(), "operation not permitted") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if readKid(t, repoPath).StoppedAt != nil {
		t.Error("the stopped mark must be taken back when the signal fails")
	}
}
