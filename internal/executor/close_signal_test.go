package executor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/confinement"
)

// fakeProc stands in for /proc, a state dir and the signal; alive lists the
// pids kill(pid, 0) would find.
type fakeProc struct {
	root, stateDir string
	signalled      []int
}

func newFakeProc(t *testing.T, alive ...int) *fakeProc {
	t.Helper()
	f := &fakeProc{root: t.TempDir(), stateDir: t.TempDir()}
	oldRoot, oldSig, oldState, oldAlive := procRoot, signalHangup, unitStateDir, pidAliveForStop
	t.Cleanup(func() { procRoot, signalHangup, unitStateDir, pidAliveForStop = oldRoot, oldSig, oldState, oldAlive })
	procRoot = f.root
	signalHangup = func(pid int) error { f.signalled = append(f.signalled, pid); return nil }
	unitStateDir = func() (string, error) { return f.stateDir, nil }
	pidAliveForStop = func(pid int) bool {
		for _, a := range alive {
			if a == pid {
				return true
			}
		}
		return false
	}
	return f
}

// process records a fake /proc/<pid> with the given cwd and environment.
func (f *fakeProc) process(t *testing.T, pid int, cwd string, env ...string) {
	t.Helper()
	dir := filepath.Join(f.root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cwd, filepath.Join(dir, "cwd")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.Join(env, "\x00")+"\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	testWorktree = "/eng/repos/r/.worktrees/w"
	testKey      = "r/w"
)

func stateWithPID(pid int) session.State {
	return session.State{PID: pid, WorktreePath: testWorktree, SessionKey: testKey}
}

func TestRequestCloseSignalsTheSessionsOwnProcess(t *testing.T) {
	for name, setup := range map[string]func(*fakeProc, *testing.T){
		"cwd inside the worktree": func(f *fakeProc, t *testing.T) { f.process(t, 4242, testWorktree+"/sub") },
		"session key in environ":  func(f *fakeProc, t *testing.T) { f.process(t, 4242, "/elsewhere", "SPINCLASS_SESSION_ID="+testKey) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeProc(t, 4242)
			setup(f, t)
			pid, note := requestCloseOf(stateWithPID(4242))
			if pid != 4242 || note != "" || len(f.signalled) != 1 {
				t.Fatalf("got pid %d note %q signalled %v, want 4242 signalled", pid, note, f.signalled)
			}
		})
	}
}

// state.json is agent-writable: a PID naming some other live process (the
// operator's shell, an unrelated program) is never signalled.
func TestRequestCloseNeverSignalsAForeignPID(t *testing.T) {
	f := newFakeProc(t, 777)
	f.process(t, 777, "/home/op", "SPINCLASS_SESSION_ID=other/session", "SPINCLASS_SESSION_IDX="+testKey)
	pid, note := requestCloseOf(stateWithPID(777))
	if pid != 0 || len(f.signalled) != 0 || !strings.Contains(note, "not provably") {
		t.Fatalf("got pid %d note %q signalled %v, want a declined signal", pid, note, f.signalled)
	}
}

// A dead session whose recorded PID was reused by another process: never
// signalled.
func TestRequestCloseNeverSignalsAReusedPID(t *testing.T) {
	f := newFakeProc(t, 4242)
	f.process(t, 4242, "/var/lib/something") // the new owner of the number
	if pid, _ := requestCloseOf(stateWithPID(4242)); pid != 0 || len(f.signalled) != 0 {
		t.Fatalf("got pid %d signalled %v, want nothing", pid, f.signalled)
	}
}

func TestRequestCloseNeverSignalsPIDZeroOrADeadPID(t *testing.T) {
	f := newFakeProc(t)
	for _, p := range []int{0, -1, 4242} {
		if pid, note := requestCloseOf(stateWithPID(p)); pid != 0 || note != "" {
			t.Errorf("pid %d: got %d %q", p, pid, note)
		}
	}
	if len(f.signalled) != 0 {
		t.Errorf("signalled %v", f.signalled)
	}
}

// A confined session is never signalled, even when its recorded PID would
// otherwise prove out: the number came from the unit's own pid namespace.
func TestRequestCloseNeverSignalsAConfinedSession(t *testing.T) {
	for _, path := range []func(string, string) string{confinement.UnitRecordPath, confinement.UnitSpecPath} {
		f := newFakeProc(t, 12)
		f.process(t, 12, testWorktree)
		rec := path(f.stateDir, testWorktree)
		if err := os.MkdirAll(filepath.Dir(rec), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rec, []byte("{not even valid"), 0o600); err != nil {
			t.Fatal(err)
		}
		pid, note := requestCloseOf(stateWithPID(12))
		if pid != 0 || len(f.signalled) != 0 || !strings.Contains(note, "confined session") {
			t.Fatalf("%s: got pid %d note %q signalled %v", rec, pid, note, f.signalled)
		}
	}
}

func TestPathWithin(t *testing.T) {
	for p, want := range map[string]bool{
		testWorktree:            true,
		testWorktree + "/a/b":   true,
		testWorktree + "-other": false,
		"/eng/repos/r":          false,
		testWorktree + "/../x":  false,
	} {
		if got := pathWithin(p, testWorktree); got != want {
			t.Errorf("pathWithin(%q) = %v, want %v", p, got, want)
		}
	}
}
