package session

// R3-M2: the worktree-local state.json is a file the worker can replace with
// anything. Every host-side load of it must neither hang (a FIFO), nor follow a
// link (to /dev/zero), nor read an unbounded file (2 MiB), and a bad file must
// make that session's load fail or be noted, never block `sc list` or the
// serve's verbs for every session.

import (
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// within runs f and fails the test when it has not returned in a few seconds.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not return: the loader blocked on a hostile state.json", what)
	}
}

type hostileFile struct {
	name  string
	plant func(t *testing.T, path string)
}

func hostileFiles() []hostileFile {
	return []hostileFile{
		{"a FIFO", func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a symlink to /dev/zero", func(t *testing.T, path string) {
			if err := os.Symlink("/dev/zero", path); err != nil {
				t.Fatal(err)
			}
		}},
		{"a 2 MiB file", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"x":"`+strings.Repeat("a", 2<<20)+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
}

func replaceDisplayCopy(t *testing.T, wt string, h hostileFile) {
	t.Helper()
	if err := os.Remove(worktreeStatePath(wt)); err != nil {
		t.Fatal(err)
	}
	h.plant(t, worktreeStatePath(wt))
}

// A hostile display copy of a session that HAS a frontend record: the record is
// used, the listing returns, and the copy is noted, never read.
func TestAHostileDisplayCopyDoesNotBlockTheListing(t *testing.T) {
	for _, h := range hostileFiles() {
		t.Run(h.name, func(t *testing.T) {
			w1, _ := twoSessions(t)
			replaceDisplayCopy(t, w1.WorktreePath, h)
			var mu sync.Mutex
			var reports []string
			defer SetReportSink(func(msg string) { mu.Lock(); reports = append(reports, msg); mu.Unlock() })()

			var got []State
			var err error
			within(t, "ListAll", func() { got, err = ListAll(nil) })
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, s := range got {
				found = found || s.Branch == "w1"
			}
			if !found {
				t.Errorf("ListAll lost the session whose display copy is %s", h.name)
			}
			mu.Lock()
			defer mu.Unlock()
			noted := false
			for _, r := range reports {
				noted = noted || strings.Contains(r, "cannot be read")
			}
			if !noted {
				t.Errorf("no note that the worktree copy (%s) cannot be read; reports %v", h.name, reports)
			}
		})
	}
}

// A session with NO frontend record loads its legacy worktree copy once. A
// hostile copy there is an unreadable record: the load fails (so the evidence
// rule refuses on it), and does not block.
func TestAHostileLegacyCopyFailsTheLoadWithoutBlocking(t *testing.T) {
	for _, h := range hostileFiles() {
		t.Run(h.name, func(t *testing.T) {
			w1, _ := twoSessions(t)
			if err := os.Remove(recordPath(w1.WorktreePath)); err != nil {
				t.Fatal(err)
			}
			replaceDisplayCopy(t, w1.WorktreePath, h)

			var err error
			within(t, "loadByWorktree", func() { _, err = loadByWorktree(w1.WorktreePath) })
			if err == nil {
				t.Fatalf("loading a legacy session whose state.json is %s succeeded", h.name)
			}
			if errors.Is(err, os.ErrNotExist) {
				t.Errorf("a hostile file reads as absent (%v): it must be an unreadable record", err)
			}
		})
	}
}
