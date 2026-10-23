package hookrun

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

func repairSweatfile(cmd string) sweatfile.Sweatfile {
	return sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Repair: &cmd}}
}

func shortenRepairKillGrace(t *testing.T, grace time.Duration) {
	t.Helper()
	old := repairKillGrace
	repairKillGrace = grace
	t.Cleanup(func() { repairKillGrace = old })
}

// readPid waits for the repair script to record a descendant's pid.
func readPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("repair never wrote %s", path)
	return 0
}

// processGone reports whether pid has exited. A zombie counts as gone: it has
// exited and only waits for whoever inherited it to reap it.
func processGone(pid int) bool {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
	return len(fields) > 0 && fields[0] == "Z"
}

func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if processGone(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("descendant %d still running %s after the repair returned (#401: the cap must kill the whole tree)", pid, within)
}

// The #401 hang: a repair whose child never exits. The cap kills it, and the
// kill reaches the backgrounded grandchild, not only the top process.
func TestRepairCapKillsTheWholeProcessTree(t *testing.T) {
	wt := t.TempDir()
	pidFile := filepath.Join(wt, "child.pid")
	sf := repairSweatfile(`sleep 600 & echo $! >` + pidFile + `; wait`)

	var buf bytes.Buffer
	start := time.Now()
	err := RepairWithCap(context.Background(), sf, wt, 200*time.Millisecond, &buf)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrRepairTimeout) {
		t.Fatalf("err = %v, want ErrRepairTimeout", err)
	}
	if !strings.Contains(err.Error(), "repair-timeout") || !strings.Contains(err.Error(), "200ms") {
		t.Errorf("the error should name the knob and the cap, got %q", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("repair ran %s; the cap should have stopped it near 200ms", elapsed)
	}
	waitGone(t, readPid(t, pidFile), 2*time.Second)
}

// A tree that ignores SIGTERM (an ignored signal is inherited, so the grandchild
// ignores it too) is still killed: after the grace the whole group gets SIGKILL.
func TestRepairCapEscalatesToSIGKILLForATreeIgnoringSIGTERM(t *testing.T) {
	shortenRepairKillGrace(t, 300*time.Millisecond)
	wt := t.TempDir()
	pidFile := filepath.Join(wt, "child.pid")
	sf := repairSweatfile(`trap "" TERM; sleep 600 & echo $! >` + pidFile + `; wait`)

	start := time.Now()
	err := RepairWithCap(context.Background(), sf, wt, 200*time.Millisecond, nil)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrRepairTimeout) {
		t.Fatalf("err = %v, want ErrRepairTimeout", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("repair ran %s; cap 200ms plus grace 300ms should bound it", elapsed)
	}
	waitGone(t, readPid(t, pidFile), 2*time.Second)
}

// A repair that fails on its own, inside the cap, keeps its own error.
func TestRepairFailureInsideTheCapIsNotATimeout(t *testing.T) {
	err := RepairWithCap(context.Background(), repairSweatfile("exit 3"), t.TempDir(), 5*time.Second, nil)
	if err == nil || errors.Is(err, ErrRepairTimeout) {
		t.Fatalf("err = %v, want the command's own failure", err)
	}
}

// A caller's cancel is the caller's, not the cap's.
func TestRepairCallerCancelIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	err := RepairWithCap(ctx, repairSweatfile("sleep 600"), t.TempDir(), time.Minute, nil)
	if err == nil || errors.Is(err, ErrRepairTimeout) {
		t.Fatalf("err = %v, want a cancel error that is not ErrRepairTimeout", err)
	}
}

func TestRepairCapDefaultIsTenMinutes(t *testing.T) {
	if DefaultRepairTimeout != 10*time.Minute {
		t.Errorf("DefaultRepairTimeout = %s, want 10m", DefaultRepairTimeout)
	}
}
