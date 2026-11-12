package hostqueue

import (
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

func ptr[T any](v T) *T { return &v }

// The queue is opt-in: no section, or a section without enable = true, is off
// with the defaults.
func TestConfigFromIsOffUnlessEnabled(t *testing.T) {
	for _, hq := range []sweatfile.HostQueue{{}, {Enable: ptr(false)}, {MaxGates: ptr(2)}} {
		cfg, on, err := ConfigFrom(hq)
		if err != nil || on {
			t.Errorf("%+v: = (on %v, err %v), want off", hq, on, err)
		}
		if hq.MaxGates == nil && cfg != DefaultConfig() {
			t.Errorf("%+v: cfg = %+v, want the defaults", hq, cfg)
		}
	}
}

func TestConfigFromReadsEveryKey(t *testing.T) {
	cfg, on, err := ConfigFrom(sweatfile.HostQueue{
		Enable: ptr(true), MaxGates: ptr(2),
		MemoryFloor: ptr("3GiB"), MemoryResume: ptr("5 GiB"),
		PressureMemoryAvg10: ptr(20.0), SwapCeiling: ptr("8GiB"), DiskFloor: ptr("50GB"),
		LoadThreshold: ptr(1.5), Settle: ptr("30s"), Poll: ptr("5s"), MaxWait: ptr("0"), IdleGrace: ptr("0"),
	})
	if err != nil || !on {
		t.Fatalf("= (on %v, err %v)", on, err)
	}
	want := Config{
		MaxGates: 2, MemoryFloor: 3 << 30, MemoryResume: 5 << 30, PressureAvg10: 20,
		SwapCeiling: 8 << 30, DiskFloor: 50e9, LoadThreshold: 1.5,
		Settle: 30 * time.Second, Poll: 5 * time.Second, MaxWait: 0, IdleGrace: 0,
		AgingInterval: DefaultAgingInterval, // fixed, not a sweatfile key
	}
	if cfg != want {
		t.Errorf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestConfigFromReportsEveryInvalidKey(t *testing.T) {
	_, on, err := ConfigFrom(sweatfile.HostQueue{
		Enable: ptr(true), MaxGates: ptr(0), MemoryFloor: ptr("lots"), Poll: ptr("0"),
		Settle: ptr("soon"), LoadThreshold: ptr(-1.0),
	})
	if !on || err == nil {
		t.Fatalf("= (on %v, err %v), want enabled with errors", on, err)
	}
	for _, key := range []string{"max-gates", "memory-floor", "poll", "settle", "load-threshold"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("err = %v, want it to name %s", err, key)
		}
	}
}

func TestConfigFromRefusesResumeBelowFloor(t *testing.T) {
	_, _, err := ConfigFrom(sweatfile.HostQueue{Enable: ptr(true), MemoryFloor: ptr("8GiB")})
	if err == nil || !strings.Contains(err.Error(), "memory-resume") {
		t.Fatalf("err = %v, want resume-below-floor", err)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{
		"4GiB": 4 << 30, "4 GiB": 4 << 30, "512MiB": 512 << 20, "1.5GiB": 3 << 29,
		"30GB": 30e9, "100": 100, "1KiB": 1024, "2TiB": 2 << 40, "7B": 7,
	} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = (%d, %v), want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "GiB", "-1GiB", "four"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) = nil error", bad)
		}
	}
}
