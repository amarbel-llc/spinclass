// Package hostload reports host CPU load and the busiest processes with the
// spinclass session each belongs to (#388), so an agent can check for a pile-up
// before a heavy build, test or merge. It reads a /proc tree rooted at an
// injectable path so tests run against a fake one.
package hostload

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultProcRoot is the real procfs mount.
const DefaultProcRoot = "/proc"

// clockTicksPerSecond is USER_HZ, which is 100 on every Linux ABI that matters.
const clockTicksPerSecond = 100

// defaultSample is the window used when Options.Sample is not positive.
const defaultSample = 500 * time.Millisecond

// Session is the part of a spinclass session needed to attribute a process:
// its key and the worktree directory it runs in.
type Session struct {
	Key          string
	WorktreePath string
}

// Proc is one busy process.
type Proc struct {
	PID        int     `json:"pid"`
	Comm       string  `json:"comm"`
	CPUPercent float64 `json:"cpu_percent"`
	Session    string  `json:"session"` // session key, "" when the process is in no session
}

// Report is the host load snapshot.
type Report struct {
	CPUs          int     `json:"cpus"`
	Load1         float64 `json:"load1"`
	Load5         float64 `json:"load5"`
	Load15        float64 `json:"load15"`
	LoadPerCPU    float64 `json:"load_per_cpu"`   // Load1 / CPUs
	Threshold     float64 `json:"threshold"`      // per-CPU 1-minute load above which the host is busy
	OverThreshold bool    `json:"over_threshold"` // LoadPerCPU > Threshold
	Top           []Proc  `json:"top"`
	SampleMS      int     `json:"sample_ms"`
}

// Options configures Collect.
type Options struct {
	ProcRoot  string // defaults to DefaultProcRoot
	Top       int    // how many busiest processes to keep
	Sample    time.Duration
	Threshold float64
	Sessions  []Session
	// Sleep waits out the sample window; nil means time.Sleep. Tests inject a
	// hook that also rewrites the fake /proc between the two samples.
	Sleep func(time.Duration)
}

// Collect reads the load averages, samples per-process CPU over opts.Sample and
// attributes the busiest processes to sessions.
func Collect(opts Options) (Report, error) {
	root := opts.ProcRoot
	if root == "" {
		root = DefaultProcRoot
	}
	sample := opts.Sample
	if sample <= 0 {
		sample = defaultSample
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	load1, load5, load15, err := readLoadavg(root)
	if err != nil {
		return Report{}, err
	}
	cpus := countCPUs(root)

	before := sampleTicks(root)
	sleep(sample)
	after := sampleTicks(root)

	rep := Report{
		CPUs:      cpus,
		Load1:     load1,
		Load5:     load5,
		Load15:    load15,
		Threshold: opts.Threshold,
		SampleMS:  int(sample.Milliseconds()),
		Top:       busiest(root, before, after, sample, opts.Top, opts.Sessions),
	}
	rep.LoadPerCPU = load1 / float64(cpus)
	rep.OverThreshold = rep.LoadPerCPU > opts.Threshold
	return rep, nil
}

func readLoadavg(root string) (l1, l5, l15 float64, err error) {
	data, err := os.ReadFile(filepath.Join(root, "loadavg"))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("reading load average: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0, fmt.Errorf("malformed loadavg %q", strings.TrimSpace(string(data)))
	}
	var loads [3]float64
	for i := range loads {
		if loads[i], err = strconv.ParseFloat(fields[i], 64); err != nil {
			return 0, 0, 0, fmt.Errorf("malformed loadavg %q: %w", strings.TrimSpace(string(data)), err)
		}
	}
	return loads[0], loads[1], loads[2], nil
}

// countCPUs counts the per-CPU lines of <root>/stat, falling back to the
// runtime's count when the file is missing or has none.
func countCPUs(root string) int {
	if data, err := os.ReadFile(filepath.Join(root, "stat")); err == nil {
		n := 0
		for _, line := range strings.Split(string(data), "\n") {
			if len(line) > 3 && strings.HasPrefix(line, "cpu") && line[3] >= '0' && line[3] <= '9' {
				n++
			}
		}
		if n > 0 {
			return n
		}
	}
	return runtime.NumCPU()
}

type procSample struct {
	comm  string
	ticks uint64
}

// sampleTicks reads utime+stime for every numeric pid dir; a process that
// vanishes or cannot be read mid-scan is skipped.
func sampleTicks(root string) map[int]procSample {
	entries, _ := os.ReadDir(root)
	out := make(map[int]procSample)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name(), "stat"))
		if err != nil {
			continue
		}
		if comm, ticks, ok := parseProcStat(string(data)); ok {
			out[pid] = procSample{comm: comm, ticks: ticks}
		}
	}
	return out
}

// parseProcStat extracts comm and utime+stime from a /proc/<pid>/stat line.
// comm is field 2 in parentheses and may itself contain spaces and parentheses,
// so the fields after it are found from the LAST ')'.
func parseProcStat(line string) (comm string, ticks uint64, ok bool) {
	open := strings.IndexByte(line, '(')
	closeIdx := strings.LastIndexByte(line, ')')
	if open < 0 || closeIdx < open {
		return "", 0, false
	}
	// Fields after the parenthesis start at field 3 (state); utime and stime
	// are fields 14 and 15, so indexes 11 and 12.
	rest := strings.Fields(line[closeIdx+1:])
	if len(rest) < 13 {
		return "", 0, false
	}
	utime, err := strconv.ParseUint(rest[11], 10, 64)
	if err != nil {
		return "", 0, false
	}
	stime, err := strconv.ParseUint(rest[12], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return line[open+1 : closeIdx], utime + stime, true
}

// busiest ranks the processes present in both samples by CPU use over the
// window, drops idle ones, keeps the top n and attributes them to sessions.
func busiest(root string, before, after map[int]procSample, window time.Duration, n int, sessions []Session) []Proc {
	var procs []Proc
	for pid, b := range before {
		a, ok := after[pid]
		if !ok || a.ticks <= b.ticks {
			continue
		}
		delta := float64(a.ticks - b.ticks)
		procs = append(procs, Proc{
			PID:        pid,
			Comm:       a.comm,
			CPUPercent: delta * 100 / (clockTicksPerSecond * window.Seconds()),
		})
	}
	sort.Slice(procs, func(i, j int) bool {
		if procs[i].CPUPercent != procs[j].CPUPercent {
			return procs[i].CPUPercent > procs[j].CPUPercent
		}
		return procs[i].PID < procs[j].PID
	})
	if n >= 0 && len(procs) > n {
		procs = procs[:n]
	}
	for i := range procs {
		cwd, err := os.Readlink(filepath.Join(root, strconv.Itoa(procs[i].PID), "cwd"))
		if err == nil {
			procs[i].Session = sessionForDir(cwd, sessions)
		}
	}
	return procs
}

// sessionForDir returns the key of the session whose worktree contains dir (the
// longest such worktree, so a nested checkout wins), or "" when none does.
func sessionForDir(dir string, sessions []Session) string {
	best, bestLen := "", -1
	for _, s := range sessions {
		wt := strings.TrimRight(s.WorktreePath, string(filepath.Separator))
		if wt == "" {
			continue
		}
		if dir != wt && !strings.HasPrefix(dir, wt+string(filepath.Separator)) {
			continue
		}
		if len(wt) > bestLen {
			best, bestLen = s.Key, len(wt)
		}
	}
	return best
}
