package hostqueue

import (
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/hostload"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// healthy passes every probe term under DefaultConfig.
func healthy() hostload.Snapshot {
	return hostload.Snapshot{
		CPUs: 8, Load1: 4, LoadPerCPU: 0.5,
		MemAvailable: 8 * gib, SwapUsed: 1 * gib,
		HasPressure: true, PressureMemorySomeAvg10: 1.0,
		HasStoreFree: true, StoreFreeBytes: 100 * gib,
	}
}

func TestDecideReasons(t *testing.T) {
	cfg := DefaultConfig()
	old := Marker{Session: "r/old", AdmittedAt: t0.Add(-10 * time.Minute)}
	fresh := Marker{Session: "r/fresh", AdmittedAt: t0.Add(-10 * time.Second)}
	cases := []struct {
		name  string
		view  View
		snap  func(*hostload.Snapshot)
		cfg   func(*Config)
		want  Reason
		admit bool
	}{
		{name: "all clear admits", admit: true},
		{name: "hold", view: View{Held: true, HoldReason: "deploy"}, want: ReasonHold},
		{name: "gate cap", view: View{Live: []Marker{old}}, want: ReasonGates},
		{name: "cap of two admits a second", view: View{Live: []Marker{old}}, cfg: func(c *Config) { c.MaxGates = 2 }, admit: true},
		{name: "memory floor", snap: func(s *hostload.Snapshot) { s.MemAvailable = 3 * gib }, want: ReasonMemory},
		{name: "pressure", snap: func(s *hostload.Snapshot) { s.PressureMemorySomeAvg10 = 25 }, want: ReasonPressure},
		{name: "swap ignored when PSI exists", snap: func(s *hostload.Snapshot) { s.SwapUsed = 10 * gib }, admit: true},
		{name: "swap ceiling without PSI", snap: func(s *hostload.Snapshot) { s.HasPressure = false; s.SwapUsed = 10 * gib }, want: ReasonSwap},
		{name: "disk floor", snap: func(s *hostload.Snapshot) { s.StoreFreeBytes = 10 * gib }, want: ReasonDisk},
		{name: "unreadable disk does not hold", snap: func(s *hostload.Snapshot) { s.HasStoreFree = false; s.StoreFreeBytes = 0 }, admit: true},
		{name: "load", snap: func(s *hostload.Snapshot) { s.LoadPerCPU = 2.7 }, want: ReasonLoad},
		{name: "settle after a fresh admission", view: View{Live: []Marker{fresh}}, cfg: func(c *Config) { c.MaxGates = 2 }, want: ReasonSettle},
		{name: "hold wins over everything", view: View{Held: true, Live: []Marker{old}}, snap: func(s *hostload.Snapshot) { s.MemAvailable = 0 }, want: ReasonHold},
		{name: "gate cap wins over probe", view: View{Live: []Marker{old}}, snap: func(s *hostload.Snapshot) { s.LoadPerCPU = 9 }, want: ReasonGates},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap, conf := healthy(), cfg
			if c.snap != nil {
				c.snap(&snap)
			}
			if c.cfg != nil {
				c.cfg(&conf)
			}
			d := Decide(c.view, snap, conf, WaiterState{}, t0)
			if d.Admit != c.admit || (!c.admit && d.Reason != c.want) {
				t.Fatalf("= %+v, want admit=%v reason=%q", d, c.admit, c.want)
			}
			if !d.Admit && d.Detail == "" {
				t.Error("a held waiter must get a detail line")
			}
		})
	}
}

func TestDecideHoldNamesTheReason(t *testing.T) {
	d := Decide(View{Held: true, HoldReason: "relock in progress"}, healthy(), DefaultConfig(), WaiterState{}, t0)
	if !strings.Contains(d.Detail, "relock in progress") {
		t.Errorf("detail = %q, want the hold reason", d.Detail)
	}
}

// Memory hysteresis: refused below the floor, the waiter then needs the resume
// line; above it, the hysteresis clears.
func TestDecideMemoryHysteresis(t *testing.T) {
	cfg := DefaultConfig()
	snap := healthy()
	snap.MemAvailable = 3 * gib
	d := Decide(View{}, snap, cfg, WaiterState{}, t0)
	if d.Admit || !d.Next.HeldForMemory {
		t.Fatalf("below floor: %+v", d)
	}
	snap.MemAvailable = 5 * gib // above floor, below resume
	d = Decide(View{}, snap, cfg, d.Next, t0.Add(time.Minute))
	if d.Admit || d.Reason != ReasonMemory || !strings.Contains(d.Detail, "resume line") {
		t.Fatalf("between floor and resume: %+v", d)
	}
	snap.MemAvailable = 7 * gib
	d = Decide(View{}, snap, cfg, d.Next, t0.Add(2*time.Minute))
	if !d.Admit || d.Next.HeldForMemory {
		t.Fatalf("above resume: %+v", d)
	}
	// A waiter never held for memory needs only the floor.
	snap.MemAvailable = 5 * gib
	if d := Decide(View{}, snap, cfg, WaiterState{}, t0); !d.Admit {
		t.Fatalf("fresh waiter above floor: %+v", d)
	}
}

// idle-grace: a probe term failing for idle-grace with no gate running admits
// with Grace; a live gate, a hold, or idle-grace 0 keeps waiting.
func TestDecideIdleGrace(t *testing.T) {
	cfg := DefaultConfig()
	snap := healthy()
	snap.LoadPerCPU = 3
	d := Decide(View{}, snap, cfg, WaiterState{}, t0)
	if d.Admit || d.Next.ProbeFailingSince != t0 {
		t.Fatalf("first failure: %+v", d)
	}
	d = Decide(View{}, snap, cfg, d.Next, t0.Add(cfg.IdleGrace-time.Second))
	if d.Admit {
		t.Fatalf("before idle-grace: %+v", d)
	}
	d = Decide(View{}, snap, cfg, d.Next, t0.Add(cfg.IdleGrace))
	if !d.Admit || !d.Grace || !strings.Contains(d.Detail, "idle-grace") {
		t.Fatalf("at idle-grace: %+v", d)
	}

	failing := WaiterState{ProbeFailingSince: t0}
	cfg2 := cfg
	cfg2.MaxGates = 2
	if d := Decide(View{Live: []Marker{{Session: "r/x", AdmittedAt: t0}}}, snap, cfg2, failing, t0.Add(time.Hour)); d.Admit {
		t.Fatalf("a running gate blocks idle-grace: %+v", d)
	}
	if d := Decide(View{Held: true}, snap, cfg, failing, t0.Add(time.Hour)); d.Admit {
		t.Fatalf("a hold blocks idle-grace: %+v", d)
	}
	cfg.IdleGrace = 0
	if d := Decide(View{}, snap, cfg, failing, t0.Add(time.Hour)); d.Admit {
		t.Fatalf("idle-grace 0 is off: %+v", d)
	}
}

// A passing probe resets the failing clock, so idle-grace measures continuous
// failure only.
func TestDecidePassingProbeResetsTheGraceClock(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxGates = 2
	d := Decide(View{Live: []Marker{{Session: "r/x", AdmittedAt: t0}}}, healthy(), cfg, WaiterState{ProbeFailingSince: t0}, t0.Add(10*time.Second))
	if d.Reason != ReasonSettle || !d.Next.ProbeFailingSince.IsZero() {
		t.Fatalf("= %+v, want settle with the clock reset", d)
	}
}
