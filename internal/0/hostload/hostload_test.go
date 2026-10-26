package hostload

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeLoadavg(t *testing.T, root, load string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "loadavg"), load+" 2/300 12345\n")
}

func writeCPUs(t *testing.T, root string, n int) {
	t.Helper()
	stat := "cpu  1 2 3 4 5 6 7 8 9 10\n"
	for i := 0; i < n; i++ {
		stat += fmt.Sprintf("cpu%d 1 2 3 4 5 6 7 8 9 10\n", i)
	}
	stat += "intr 1 2 3\nctxt 4\n"
	writeFile(t, filepath.Join(root, "stat"), stat)
}

// writeProcStat writes a pid's stat with the given cumulative utime and stime;
// comm goes in verbatim between the parentheses.
func writeProcStat(t *testing.T, root string, pid int, comm string, utime, stime int) {
	t.Helper()
	content := fmt.Sprintf("%d (%s) S 1 1 1 0 -1 0 0 0 0 0 %d %d 0 0 20 0 1 0 0 0 0\n", pid, comm, utime, stime)
	writeFile(t, filepath.Join(root, fmt.Sprint(pid), "stat"), content)
}

func linkCwd(t *testing.T, root string, pid int, target string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(root, fmt.Sprint(pid), "cwd")); err != nil {
		t.Fatal(err)
	}
}

func noSleep(time.Duration) {}

func TestLoadParsingAndThreshold(t *testing.T) {
	root := t.TempDir()
	writeLoadavg(t, root, "6.00 3.50 1.25")
	writeCPUs(t, root, 4)

	rep, err := Collect(Options{ProcRoot: root, Top: 5, Sample: time.Second, Threshold: 1.0, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	if rep.CPUs != 4 {
		t.Errorf("CPUs = %d, want 4", rep.CPUs)
	}
	if rep.Load1 != 6.0 || rep.Load5 != 3.5 || rep.Load15 != 1.25 {
		t.Errorf("loads = %v %v %v, want 6 3.5 1.25", rep.Load1, rep.Load5, rep.Load15)
	}
	if rep.LoadPerCPU != 1.5 {
		t.Errorf("LoadPerCPU = %v, want 1.5", rep.LoadPerCPU)
	}
	if !rep.OverThreshold || rep.Threshold != 1.0 {
		t.Errorf("OverThreshold = %v Threshold = %v, want true 1", rep.OverThreshold, rep.Threshold)
	}
	if rep.SampleMS != 1000 {
		t.Errorf("SampleMS = %d, want 1000", rep.SampleMS)
	}

	rep, err = Collect(Options{ProcRoot: root, Top: 5, Sample: time.Second, Threshold: 1.5, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OverThreshold {
		t.Errorf("load per cpu equal to the threshold must not be over it")
	}
}

func TestCPUCountFallsBackWithoutStat(t *testing.T) {
	root := t.TempDir()
	writeLoadavg(t, root, "0.10 0.10 0.10")

	rep, err := Collect(Options{ProcRoot: root, Top: 5, Sample: time.Second, Threshold: 1.0, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	if rep.CPUs < 1 {
		t.Errorf("CPUs = %d, want the runtime fallback (>= 1)", rep.CPUs)
	}
}

func TestMissingLoadavgIsAnError(t *testing.T) {
	if _, err := Collect(Options{ProcRoot: t.TempDir(), Top: 5, Sample: time.Second, Sleep: noSleep}); err == nil {
		t.Fatal("want an error when loadavg is unreadable")
	}
}

func TestCommWithSpacesAndParens(t *testing.T) {
	root := t.TempDir()
	writeLoadavg(t, root, "1 1 1")
	writeCPUs(t, root, 1)
	const comm = "weird (name) x"
	writeProcStat(t, root, 10, comm, 0, 0)

	rep, err := Collect(Options{ProcRoot: root, Top: 5, Sample: time.Second, Sleep: func(time.Duration) {
		writeProcStat(t, root, 10, comm, 30, 20)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Top) != 1 {
		t.Fatalf("Top = %+v, want one process", rep.Top)
	}
	got := rep.Top[0]
	if got.PID != 10 || got.Comm != comm {
		t.Errorf("got pid %d comm %q, want 10 %q", got.PID, got.Comm, comm)
	}
	// 50 ticks over 1s at 100 ticks/s is 50%.
	if got.CPUPercent != 50 {
		t.Errorf("CPUPercent = %v, want 50", got.CPUPercent)
	}
}

func TestTopNOrderingAndZeroDeltaDrop(t *testing.T) {
	root := t.TempDir()
	writeLoadavg(t, root, "1 1 1")
	writeCPUs(t, root, 2)
	for pid := 1; pid <= 4; pid++ {
		writeProcStat(t, root, pid, fmt.Sprintf("p%d", pid), 100, 0)
	}

	rep, err := Collect(Options{ProcRoot: root, Top: 2, Sample: 2 * time.Second, Sleep: func(time.Duration) {
		writeProcStat(t, root, 1, "p1", 100, 0)  // idle: dropped
		writeProcStat(t, root, 2, "p2", 150, 0)  // +50 ticks
		writeProcStat(t, root, 3, "p3", 100, 90) // +90 ticks
		writeProcStat(t, root, 4, "p4", 110, 0)  // +10 ticks, cut by Top
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Top) != 2 {
		t.Fatalf("Top = %+v, want 2 entries", rep.Top)
	}
	if rep.Top[0].PID != 3 || rep.Top[1].PID != 2 {
		t.Errorf("order = %d, %d, want 3, 2", rep.Top[0].PID, rep.Top[1].PID)
	}
	// 90 ticks over a 2s window is 45%.
	if rep.Top[0].CPUPercent != 45 {
		t.Errorf("top CPUPercent = %v, want 45", rep.Top[0].CPUPercent)
	}

	rep, err = Collect(Options{ProcRoot: root, Top: 10, Sample: time.Second, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Top) != 0 {
		t.Errorf("Top = %+v, want none when nothing moved", rep.Top)
	}
}

func TestSessionAttribution(t *testing.T) {
	root := t.TempDir()
	writeLoadavg(t, root, "1 1 1")
	writeCPUs(t, root, 1)

	work := t.TempDir()
	wtA := filepath.Join(work, "repo", ".worktrees", "a")
	wtAB := filepath.Join(work, "repo", ".worktrees", "ab") // shares a string prefix with a
	sub := filepath.Join(wtA, "internal", "pkg")
	outside := filepath.Join(work, "elsewhere")
	for _, d := range []string{wtA, wtAB, sub, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cwds := map[int]string{1: wtA, 2: sub, 3: wtAB, 4: outside}
	for pid, cwd := range cwds {
		writeProcStat(t, root, pid, fmt.Sprintf("p%d", pid), 0, 0)
		linkCwd(t, root, pid, cwd)
	}
	// pid 5 has no readable cwd.
	writeProcStat(t, root, 5, "p5", 0, 0)

	rep, err := Collect(Options{
		ProcRoot: root, Top: 10, Sample: time.Second,
		Sessions: []Session{
			{Key: "repo/a", WorktreePath: wtA},
			{Key: "repo/ab", WorktreePath: wtAB},
		},
		Sleep: func(time.Duration) {
			for pid := 1; pid <= 5; pid++ {
				writeProcStat(t, root, pid, fmt.Sprintf("p%d", pid), 10*pid, 0)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{1: "repo/a", 2: "repo/a", 3: "repo/ab", 4: "", 5: ""}
	if len(rep.Top) != len(want) {
		t.Fatalf("Top = %+v, want %d entries", rep.Top, len(want))
	}
	for _, p := range rep.Top {
		if p.Session != want[p.PID] {
			t.Errorf("pid %d session = %q, want %q", p.PID, p.Session, want[p.PID])
		}
	}
}

func TestVanishedPIDBetweenSamplesIsSkipped(t *testing.T) {
	root := t.TempDir()
	writeLoadavg(t, root, "1 1 1")
	writeCPUs(t, root, 1)
	writeProcStat(t, root, 1, "stays", 0, 0)
	writeProcStat(t, root, 2, "goes", 0, 0)

	rep, err := Collect(Options{ProcRoot: root, Top: 10, Sample: time.Second, Sleep: func(time.Duration) {
		if err := os.RemoveAll(filepath.Join(root, "2")); err != nil {
			t.Fatal(err)
		}
		writeProcStat(t, root, 1, "stays", 25, 0)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Top) != 1 || rep.Top[0].PID != 1 {
		t.Errorf("Top = %+v, want only pid 1", rep.Top)
	}
}
