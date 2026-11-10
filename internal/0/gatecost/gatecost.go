// Package gatecost is the pure half of per-gate cost tracking (#424 step 0,
// docs/plans/2026-11-10-424-host-admission-queue-follow-up.md §2 and §9b): the
// cgroup v2 file parsers, the failing-just-recipe log parse, and the
// history.ndjson record and its append. It records only; nothing here changes
// when or whether a gate runs. The wiring (reading the scope's accounting,
// emitting statsd, appending history) lives where the gate runs, in
// internal/bravo/hookrun and internal/charlie/check.
package gatecost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Gate kinds, the <kind> segment of the gate.<repo>.<kind>.* metrics and the
// record's kind field.
const (
	KindMerge   = "merge"
	KindCheck   = "check"
	KindGuarded = "guarded"
)

// Scope is the gate scope cgroup's accounting, sampled from outside the scope
// while the hook runs (hookrun's scope sampler). A nil field was not available
// (the controller is not delegated, the hook ran unscoped, or the value did not
// parse).
type Scope struct {
	CPUUsec      *uint64
	MemPeakBytes *uint64
}

// ParseCPUStatUsageUsec returns the usage_usec value of a cgroup v2 cpu.stat.
func ParseCPUStatUsageUsec(cpuStat []byte) (uint64, bool) {
	for _, line := range strings.Split(string(cpuStat), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && key == "usage_usec" {
			return parseUint(value)
		}
	}
	return 0, false
}

// ParseMemoryPeak returns a cgroup v2 memory.peak value in bytes.
func ParseMemoryPeak(memoryPeak []byte) (uint64, bool) {
	return parseUint(string(memoryPeak))
}

func parseUint(s string) (uint64, bool) {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return v, err == nil
}

// CgroupV2Path returns the unified cgroup a /proc/<pid>/cgroup file names (its
// `0::<path>` line). The root cgroup (host-wide counters), a relative path and
// a path with ".." are no answer.
func CgroupV2Path(procCgroup []byte) (string, bool) {
	for _, line := range strings.Split(string(procCgroup), "\n") {
		path, ok := strings.CutPrefix(strings.TrimSpace(line), "0::")
		if !ok {
			continue
		}
		if !strings.HasPrefix(path, "/") || path == "/" || strings.Contains(path, "..") {
			return "", false
		}
		return path, true
	}
	return "", false
}

// ScopeMarkerProcCgroup prefixes the /proc/self/cgroup lines the scope wrapper
// copies into its start marker before it execs the hook (hookrun's
// scopeStartScript writes them).
const ScopeMarkerProcCgroup = "proc-cgroup "

// ScopeCgroupPath returns the scope's cgroup from the start marker's copied
// /proc/self/cgroup lines.
func ScopeCgroupPath(marker []byte) (string, bool) {
	var proc bytes.Buffer
	for _, line := range strings.Split(string(marker), "\n") {
		if rest, ok := strings.CutPrefix(line, ScopeMarkerProcCgroup); ok {
			proc.WriteString(rest + "\n")
		}
	}
	return CgroupV2Path(proc.Bytes())
}

// ReadScopeCgroup samples memory.peak and cpu.stat usage_usec of the cgroup at
// root+path. A file that is missing (the controller not delegated, or the
// scope already gone) or unparseable leaves its field absent.
func ReadScopeCgroup(root, path string) Scope {
	var s Scope
	dir := filepath.Join(root, path)
	if v, ok := ReadCPUUsageUsec(filepath.Join(dir, "cpu.stat")); ok {
		s.CPUUsec = &v
	}
	if data, err := os.ReadFile(filepath.Join(dir, "memory.peak")); err == nil {
		if v, ok := ParseMemoryPeak(data); ok {
			s.MemPeakBytes = &v
		}
	}
	return s
}

// Update is s with every field newer read, so a sample that failed (the scope
// already gone) keeps the last good value.
func (s Scope) Update(newer Scope) Scope {
	if newer.CPUUsec != nil {
		s.CPUUsec = newer.CPUUsec
	}
	if newer.MemPeakBytes != nil {
		s.MemPeakBytes = newer.MemPeakBytes
	}
	return s
}

// Disabled reports the SPINCLASS_DISABLE_GATE_COST=1 kill switch: nothing is
// sampled or recorded (the SPINCLASS_DISABLE_STATSD idiom; tests set it so a
// fixture gate never writes the real history).
func Disabled() bool {
	return os.Getenv("SPINCLASS_DISABLE_GATE_COST") == "1"
}

// ReadCPUUsageUsec reads usage_usec from the cpu.stat at path (the
// nix-daemon.service cgroup's, read before and after a gate). A missing or
// unparseable file is no value.
func ReadCPUUsageUsec(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	return ParseCPUStatUsageUsec(data)
}

var (
	ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
	// just's failure line, e.g. "error: Recipe `test-nix` failed on line 139
	// with exit code 1" (the capital R varies by just version). Anchored at the
	// line start so a hook echoing the text mid-line does not count.
	justRecipeFailed = regexp.MustCompile("(?m)^error: [Rr]ecipe `([^`]+)` failed on line [0-9]+")
)

// LastFailedRecipe returns the recipe named by the last just failure line in a
// gate's output, or "" when there is none (§9b option 1: a parse of output
// already captured, dependent on just's message format).
func LastFailedRecipe(output string) string {
	matches := justRecipeFailed.FindAllStringSubmatch(ansiEscape.ReplaceAllString(output, ""), -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

// Record is one gate's cost, one history.ndjson line. Nil pointers and empty
// strings are absent and omitted; Wall is always written.
type Record struct {
	TS            time.Time
	Repo          string
	Kind          string
	Caller        string
	Outcome       string
	Wall          time.Duration
	CPUUsec       *uint64
	MemPeakBytes  *uint64
	DaemonCPUUsec *uint64
	FailedRecipe  string
	Session       string
}

type recordLine struct {
	TS            string  `json:"ts"`
	Repo          string  `json:"repo"`
	Kind          string  `json:"kind"`
	Caller        string  `json:"caller,omitempty"`
	Outcome       string  `json:"outcome"`
	WallMS        int64   `json:"wall_ms"`
	CPUUsec       *uint64 `json:"cpu_usec,omitempty"`
	MemPeakBytes  *uint64 `json:"mem_peak_bytes,omitempty"`
	DaemonCPUUsec *uint64 `json:"daemon_cpu_usec,omitempty"`
	FailedRecipe  string  `json:"failed_recipe,omitempty"`
	Session       string  `json:"session,omitempty"`
}

// Line encodes r as one newline-terminated JSON line, ts in RFC 3339 UTC.
func (r Record) Line() ([]byte, error) {
	b, err := json.Marshal(recordLine{
		TS:            r.TS.UTC().Format(time.RFC3339),
		Repo:          r.Repo,
		Kind:          r.Kind,
		Caller:        r.Caller,
		Outcome:       r.Outcome,
		WallMS:        r.Wall.Milliseconds(),
		CPUUsec:       r.CPUUsec,
		MemPeakBytes:  r.MemPeakBytes,
		DaemonCPUUsec: r.DaemonCPUUsec,
		FailedRecipe:  r.FailedRecipe,
		Session:       r.Session,
	})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// HistoryPath is $XDG_STATE_HOME/spinclass/host-queue/history.ndjson, the base
// defaulting to $HOME/.local/state (the rebaserecord.Dir convention).
func HistoryPath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "spinclass", "host-queue", "history.ndjson")
}

// AppendHistory appends r to the history file at path in one O_APPEND write,
// creating its directory 0700 when missing.
func AppendHistory(path string, r Record) error {
	line, err := r.Line()
	if err != nil {
		return fmt.Errorf("encoding the gate cost record: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// TODO(#424): rotate history.ndjson
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
