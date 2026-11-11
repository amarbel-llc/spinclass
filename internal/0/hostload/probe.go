package hostload

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// DefaultStorePath is the filesystem whose free space the probe reads: nix
// builds write there, so a full one fails gates (#430).
const DefaultStorePath = "/nix/store"

// Snapshot is what the host admission probe sees (#424 step 1): the load, the
// memory, the memory pressure (PSI) or the swap in use where PSI is absent, and
// the store filesystem's free space. Unlike Collect, it takes no per-process
// sample, so it is cheap enough to run every poll.
type Snapshot struct {
	CPUs       int     `json:"cpus"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	LoadPerCPU float64 `json:"load_per_cpu"` // Load1 / CPUs

	MemAvailable uint64 `json:"mem_available_bytes"`
	SwapUsed     uint64 `json:"swap_used_bytes"`

	// HasPressure is false where /proc/pressure/memory does not exist; the
	// caller then judges memory pressure by SwapUsed instead.
	HasPressure             bool    `json:"has_pressure"`
	PressureMemorySomeAvg10 float64 `json:"pressure_memory_some_avg10"`

	// HasStoreFree is false when the store filesystem could not be read; the
	// disk term then cannot hold a gate.
	HasStoreFree   bool   `json:"has_store_free"`
	StoreFreeBytes uint64 `json:"store_free_bytes"`
}

// ProbeOptions configures Probe. Every field has a real default; tests inject a
// fake /proc and a fake free-space reader.
type ProbeOptions struct {
	ProcRoot  string                       // defaults to DefaultProcRoot
	StorePath string                       // defaults to DefaultStorePath
	FreeBytes func(string) (uint64, error) // defaults to statfs's available bytes
}

// Probe reads the host's state once. The load average and MemAvailable are
// required: without them the predicate cannot be judged, and guessing would
// admit a gate into a full host. Pressure and store free space are optional and
// reported as absent.
func Probe(opts ProbeOptions) (Snapshot, error) {
	root := opts.ProcRoot
	if root == "" {
		root = DefaultProcRoot
	}
	store := opts.StorePath
	if store == "" {
		store = DefaultStorePath
	}
	freeBytes := opts.FreeBytes
	if freeBytes == nil {
		freeBytes = statfsAvailable
	}

	load1, load5, _, err := readLoadavg(root)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{CPUs: countCPUs(root), Load1: load1, Load5: load5}
	snap.LoadPerCPU = load1 / float64(snap.CPUs)

	mem, err := readMeminfo(root)
	if err != nil {
		return Snapshot{}, err
	}
	available, ok := mem["MemAvailable"]
	if !ok {
		return Snapshot{}, fmt.Errorf("reading memory: %s has no MemAvailable line", filepath.Join(root, "meminfo"))
	}
	snap.MemAvailable = available
	if total, free := mem["SwapTotal"], mem["SwapFree"]; total > free {
		snap.SwapUsed = total - free
	}

	if data, err := os.ReadFile(filepath.Join(root, "pressure", "memory")); err == nil {
		snap.PressureMemorySomeAvg10, snap.HasPressure = parsePressureSomeAvg10(data)
	}

	if free, err := freeBytes(store); err == nil {
		snap.StoreFreeBytes, snap.HasStoreFree = free, true
	}
	return snap, nil
}

// readMeminfo reads <root>/meminfo into bytes per key; values the kernel gives
// in kB are scaled, unit-less ones are kept as they are.
func readMeminfo(root string) (map[string]uint64, error) {
	data, err := os.ReadFile(filepath.Join(root, "meminfo"))
	if err != nil {
		return nil, fmt.Errorf("reading memory: %w", err)
	}
	out := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) > 1 && fields[1] == "kB" {
			v *= 1024
		}
		out[strings.TrimSpace(key)] = v
	}
	return out, nil
}

// parsePressureSomeAvg10 reads the `some` line's avg10 from a PSI file: the
// share of the last ten seconds in which at least one task stalled on memory.
func parsePressureSomeAvg10(data []byte) (float64, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "some" {
			continue
		}
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(f, "avg10="); ok {
				n, err := strconv.ParseFloat(v, 64)
				return n, err == nil
			}
		}
		return 0, false
	}
	return 0, false
}

// statfsAvailable is the bytes an unprivileged writer may still use on path's
// filesystem.
func statfsAvailable(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
