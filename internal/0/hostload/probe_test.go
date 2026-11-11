package hostload

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// #424 step 1: the admission probe reads load, memory, memory pressure, swap
// and the store filesystem's free space, with no per-process sample.

const meminfoFixture = `MemTotal:       16368832 kB
MemFree:         1918748 kB
MemAvailable:    5843828 kB
Buffers:          123456 kB
SwapTotal:      24769528 kB
SwapFree:       21564996 kB
`

const pressureFixture = `some avg10=12.50 avg60=3.10 avg300=0.80 total=123456
full avg10=4.00 avg60=1.00 avg300=0.20 total=6543
`

func probeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeLoadavg(t, root, "17.80 10.58 6.45")
	writeCPUs(t, root, 8)
	writeFile(t, filepath.Join(root, "meminfo"), meminfoFixture)
	writeFile(t, filepath.Join(root, "pressure", "memory"), pressureFixture)
	return root
}

func fakeStatfs(free uint64, err error) func(string) (uint64, error) {
	return func(string) (uint64, error) { return free, err }
}

func TestProbeReadsEveryTerm(t *testing.T) {
	root := probeFixture(t)
	var asked string
	snap, err := Probe(ProbeOptions{ProcRoot: root, StorePath: "/nix/store", FreeBytes: func(p string) (uint64, error) {
		asked = p
		return 40 << 30, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if snap.CPUs != 8 || snap.Load1 != 17.80 || snap.Load5 != 10.58 {
		t.Errorf("load = %+v", snap)
	}
	if got, want := snap.LoadPerCPU, 17.80/8; got != want {
		t.Errorf("LoadPerCPU = %v, want %v", got, want)
	}
	if snap.MemAvailable != 5843828*1024 {
		t.Errorf("MemAvailable = %d", snap.MemAvailable)
	}
	if snap.SwapUsed != (24769528-21564996)*1024 {
		t.Errorf("SwapUsed = %d", snap.SwapUsed)
	}
	if !snap.HasPressure || snap.PressureMemorySomeAvg10 != 12.50 {
		t.Errorf("pressure = (%v, %v), want (true, 12.50)", snap.HasPressure, snap.PressureMemorySomeAvg10)
	}
	if asked != "/nix/store" || !snap.HasStoreFree || snap.StoreFreeBytes != 40<<30 {
		t.Errorf("store free = (%q, %v, %d)", asked, snap.HasStoreFree, snap.StoreFreeBytes)
	}
}

// PSI is absent on kernels without it (and in some containers): the probe
// reports that, so the caller falls back to the swap term.
func TestProbeWithoutPressureFallsBack(t *testing.T) {
	root := t.TempDir()
	writeLoadavg(t, root, "1.00 1.00 1.00")
	writeCPUs(t, root, 2)
	writeFile(t, filepath.Join(root, "meminfo"), meminfoFixture)
	snap, err := Probe(ProbeOptions{ProcRoot: root, FreeBytes: fakeStatfs(0, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if snap.HasPressure {
		t.Errorf("HasPressure = true with no pressure file")
	}
}

// A store filesystem that cannot be read is reported, not fatal: the disk term
// then cannot hold a gate.
func TestProbeStoreFreeErrorIsNotFatal(t *testing.T) {
	snap, err := Probe(ProbeOptions{ProcRoot: probeFixture(t), FreeBytes: fakeStatfs(0, errors.New("no such filesystem"))})
	if err != nil {
		t.Fatal(err)
	}
	if snap.HasStoreFree {
		t.Errorf("HasStoreFree = true after a statfs error")
	}
}

// meminfo is required: without MemAvailable the memory term cannot be judged,
// and guessing would admit into a full host.
func TestProbeWithoutMemAvailableIsAnError(t *testing.T) {
	root := t.TempDir()
	writeLoadavg(t, root, "1.00 1.00 1.00")
	writeCPUs(t, root, 2)
	writeFile(t, filepath.Join(root, "meminfo"), "MemTotal: 1 kB\n")
	if _, err := Probe(ProbeOptions{ProcRoot: root, FreeBytes: fakeStatfs(0, nil)}); err == nil || !strings.Contains(err.Error(), "MemAvailable") {
		t.Fatalf("err = %v, want one naming MemAvailable", err)
	}
}

func TestProbeWithoutLoadavgIsAnError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "meminfo"), meminfoFixture)
	if _, err := Probe(ProbeOptions{ProcRoot: root, FreeBytes: fakeStatfs(0, nil)}); err == nil {
		t.Fatal("err = nil, want the loadavg error")
	}
}

func TestParsePressureSomeAvg10(t *testing.T) {
	for _, c := range []struct {
		in   string
		want float64
		ok   bool
	}{
		{pressureFixture, 12.50, true},
		{"some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n", 0, true},
		{"full avg10=4.00\n", 0, false},
		{"some avg60=1.00\n", 0, false},
		{"some avg10=x\n", 0, false},
		{"", 0, false},
	} {
		got, ok := parsePressureSomeAvg10([]byte(c.in))
		if got != c.want || ok != c.ok {
			t.Errorf("parsePressureSomeAvg10(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
