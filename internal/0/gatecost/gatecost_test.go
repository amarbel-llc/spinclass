package gatecost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCPUStatUsageUsec(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want uint64
		ok   bool
	}{
		{"cgroup v2 cpu.stat", "usage_usec 123456\nuser_usec 100000\nsystem_usec 23456\n", 123456, true},
		{"usage not first", "nr_periods 0\nusage_usec 42\n", 42, true},
		{"no trailing newline", "usage_usec 7", 7, true},
		{"empty", "", 0, false},
		{"missing key", "user_usec 5\nsystem_usec 6\n", 0, false},
		{"garbage value", "usage_usec lots\n", 0, false},
		{"negative value", "usage_usec -3\n", 0, false},
		{"lookalike key", "usage_usec_total 9\n", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseCPUStatUsageUsec([]byte(tc.in))
			if got != tc.want || ok != tc.ok {
				t.Errorf("ParseCPUStatUsageUsec(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestParseMemoryPeak(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want uint64
		ok   bool
	}{
		{"value", "2147483648\n", 2147483648, true},
		{"no newline", "4096", 4096, true},
		{"empty", "", 0, false},
		{"max", "max\n", 0, false},
		{"garbage", "12 MB\n", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseMemoryPeak([]byte(tc.in))
			if got != tc.want || ok != tc.ok {
				t.Errorf("ParseMemoryPeak(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCgroupV2Path(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{"unified", "0::/user.slice/gate.scope\n", "/user.slice/gate.scope", true},
		{"hybrid host", "12:cpu,cpuacct:/elsewhere\n0::/user.slice/gate.scope\n1:name=systemd:/x", "/user.slice/gate.scope", true},
		{"no trailing newline", "0::/a.scope", "/a.scope", true},
		{"empty", "", "", false},
		{"blank line", "\n", "", false},
		{"garbage", "garbage\n:::\n", "", false},
		{"cgroup v1 only", "12:cpu,cpuacct:/user.slice/gate.scope\n", "", false},
		{"root cgroup", "0::/\n", "", false},
		{"relative path", "0::user.slice\n", "", false},
		{"dot-dot", "0::/user.slice/../system.slice\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CgroupV2Path([]byte(tc.in))
			if got != tc.want || ok != tc.ok {
				t.Errorf("CgroupV2Path(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The scope wrapper copies /proc/self/cgroup into the start marker, after the
// "started" line, each line prefixed `proc-cgroup `.
func TestScopeCgroupPathFromMarker(t *testing.T) {
	got, ok := ScopeCgroupPath([]byte("started\nproc-cgroup 12:cpu:/v1\nproc-cgroup 0::/user.slice/gate.scope\n"))
	if !ok || got != "/user.slice/gate.scope" {
		t.Errorf("ScopeCgroupPath = (%q, %v), want /user.slice/gate.scope", got, ok)
	}
	for _, in := range []string{"", "started\n", "started\n0::/not-prefixed\n", "started\nproc-cgroup 0::/\n"} {
		if p, ok := ScopeCgroupPath([]byte(in)); ok {
			t.Errorf("ScopeCgroupPath(%q) = %q, want none", in, p)
		}
	}
}

func TestReadScopeCgroup(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "user.slice", "gate.scope")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ReadScopeCgroup(root, "/user.slice/gate.scope"); got.CPUUsec != nil || got.MemPeakBytes != nil {
		t.Errorf("no files: got %+v, want both absent", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 31\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadScopeCgroup(root, "/user.slice/gate.scope"); got.MemPeakBytes != nil || got.CPUUsec == nil || *got.CPUUsec != 31 {
		t.Errorf("cpu only: got %+v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.peak"), []byte("8192\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadScopeCgroup(root, "/user.slice/gate.scope"); got.MemPeakBytes == nil || *got.MemPeakBytes != 8192 {
		t.Errorf("mem: got %+v", got)
	}
}

// A sample that could not read a field keeps the last good value.
func TestScopeUpdateKeepsTheLastGoodSample(t *testing.T) {
	var s Scope
	s = s.Update(Scope{CPUUsec: u64(1), MemPeakBytes: u64(10)})
	s = s.Update(Scope{CPUUsec: u64(5)})
	s = s.Update(Scope{})
	if s.CPUUsec == nil || *s.CPUUsec != 5 || s.MemPeakBytes == nil || *s.MemPeakBytes != 10 {
		t.Errorf("got %+v, want cpu 5 and mem 10", s)
	}
}

func TestDisabled(t *testing.T) {
	t.Setenv("SPINCLASS_DISABLE_GATE_COST", "1")
	if !Disabled() {
		t.Error("SPINCLASS_DISABLE_GATE_COST=1 does not disable")
	}
	t.Setenv("SPINCLASS_DISABLE_GATE_COST", "")
	if Disabled() {
		t.Error("an empty SPINCLASS_DISABLE_GATE_COST disables")
	}
}

func TestLastFailedRecipe(t *testing.T) {
	for _, tc := range []struct {
		name, out, want string
	}{
		{"one", "building\nerror: recipe `test-nix` failed on line 139 with exit code 1\n", "test-nix"},
		{"capitalised just message", "error: Recipe `build` failed on line 12 with exit code 2\n", "build"},
		{"several, last wins", "error: recipe `lint` failed on line 3 with exit code 1\nerror: recipe `default` failed on line 9 with exit code 1\n", "default"},
		{"none", "all good\nok 1 - thing\n", ""},
		{"empty", "", ""},
		{"lookalike mid-line", "echo 'error: recipe `fake` failed on line 1'\n", ""},
		{"lookalike without line", "error: recipe `script` failed with exit code 1\n", ""},
		{"lookalike other prefix", "warning: recipe `w` failed on line 2\n", ""},
		{"ansi coloured", "\x1b[1;31merror\x1b[0m: \x1b[1mRecipe `colour` failed on line 4 with exit code 1\x1b[0m\n", "colour"},
		{"no trailing newline", "error: recipe `tail` failed on line 5 with exit code 1", "tail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LastFailedRecipe(tc.out); got != tc.want {
				t.Errorf("LastFailedRecipe(%q) = %q, want %q", tc.out, got, tc.want)
			}
		})
	}
}

func u64(v uint64) *uint64 { return &v }

func TestRecordLineFieldsAndOmission(t *testing.T) {
	ts := time.Date(2026, 11, 10, 12, 30, 0, 0, time.FixedZone("x", 3600))
	full := Record{
		TS: ts, Repo: "spinclass", Kind: KindGuarded, Caller: KindMerge, Outcome: "failed", Wall: 1500 * time.Millisecond,
		CPUUsec: u64(10), MemPeakBytes: u64(20), DaemonCPUUsec: u64(30),
		FailedRecipe: "test-nix", Session: "spinclass/slim-myrtle",
	}
	line, err := full.Line()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(line), "\n") || strings.Count(string(line), "\n") != 1 {
		t.Fatalf("line %q is not exactly one newline-terminated line", line)
	}
	var got map[string]any
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("not JSON: %v: %q", err, line)
	}
	want := map[string]any{
		"ts": "2026-11-10T11:30:00Z", "repo": "spinclass", "kind": "guarded", "caller": "merge", "outcome": "failed",
		"wall_ms": float64(1500), "cpu_usec": float64(10), "mem_peak_bytes": float64(20),
		"daemon_cpu_usec": float64(30), "failed_recipe": "test-nix", "session": "spinclass/slim-myrtle",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("field %s = %#v, want %#v", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("fields = %v, want exactly %v", got, want)
	}

	minimal := Record{TS: ts, Repo: "r", Kind: KindCheck, Outcome: "ok", Wall: 0, CPUUsec: u64(0)}
	line, err = minimal.Line()
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"caller", "mem_peak_bytes", "daemon_cpu_usec", "failed_recipe", "session"} {
		if _, ok := got[absent]; ok {
			t.Errorf("absent field %s was written: %q", absent, line)
		}
	}
	// A measured zero is a value, not an absence; wall_ms is always written.
	if got["cpu_usec"] != float64(0) || got["wall_ms"] != float64(0) {
		t.Errorf("zero values dropped: %q", line)
	}
}

func TestHistoryPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if got, want := HistoryPath(), "/state/spinclass/host-queue/history.ndjson"; got != want {
		t.Errorf("HistoryPath() = %q, want %q", got, want)
	}
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	if got, want := HistoryPath(), filepath.Join(home, ".local", "state", "spinclass", "host-queue", "history.ndjson"); got != want {
		t.Errorf("HistoryPath() fallback = %q, want %q", got, want)
	}
}

func TestAppendHistoryCreatesDirAndAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "host-queue", "history.ndjson")
	for i, kind := range []string{KindCheck, KindMerge} {
		if err := AppendHistory(path, Record{TS: time.Unix(int64(i), 0), Repo: "r", Kind: kind, Outcome: "ok"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"kind":"check"`) || !strings.Contains(lines[1], `"kind":"merge"`) {
		t.Errorf("history = %q, want two lines check then merge", data)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("history dir mode = %o, want 700", perm)
	}
}

func TestReadCPUUsageUsec(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadCPUUsageUsec(filepath.Join(dir, "missing")); ok {
		t.Error("a missing file read as a value")
	}
	p := filepath.Join(dir, "cpu.stat")
	if err := os.WriteFile(p, []byte("usage_usec 77\nuser_usec 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, ok := ReadCPUUsageUsec(p); !ok || v != 77 {
		t.Errorf("ReadCPUUsageUsec = (%d, %v), want (77, true)", v, ok)
	}
}
