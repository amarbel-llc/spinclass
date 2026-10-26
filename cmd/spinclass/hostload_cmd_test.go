package main

import (
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/hostload"
)

func TestHostLoadOptionsDefaultsAndBounds(t *testing.T) {
	opts, err := hostLoadOptions(hostLoadParams{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Top != 10 || opts.Sample != 500*time.Millisecond || opts.Threshold != 1.0 {
		t.Errorf("defaults = %+v, want top 10, 500ms, threshold 1", opts)
	}

	top, sample := 999, 999999
	opts, err = hostLoadOptions(hostLoadParams{Top: &top, SampleMS: &sample}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Top != 50 || opts.Sample != 5*time.Second {
		t.Errorf("clamped = top %d sample %v, want 50 and 5s", opts.Top, opts.Sample)
	}

	zero := 0
	if _, err := hostLoadOptions(hostLoadParams{Top: &zero}, nil); err == nil {
		t.Error("top 0 must be rejected")
	}
}

func TestFormatHostLoad(t *testing.T) {
	rep := hostload.Report{
		CPUs: 4, Load1: 6, Load5: 3.5, Load15: 1.25, LoadPerCPU: 1.5, Threshold: 1, OverThreshold: true,
		Top: []hostload.Proc{
			{PID: 42, Comm: "go", CPUPercent: 187.5, Session: "spinclass/firm-mulberry"},
			{PID: 7, Comm: "cc1", CPUPercent: 12},
		},
	}
	want := "cpus 4  load 1/5/15: 6.00 3.50 1.25  per-cpu 1.50 (threshold 1: OVER)\n" +
		"42 go 187.5% spinclass/firm-mulberry\n" +
		"7 cc1 12.0% -"
	if got := formatHostLoad(rep); got != want {
		t.Errorf("formatHostLoad =\n%s\nwant\n%s", got, want)
	}

	rep.OverThreshold = false
	if got := formatHostLoad(rep); !strings.Contains(strings.SplitN(got, "\n", 2)[0], "threshold 1: ok") {
		t.Errorf("header = %q, want the ok verdict", got)
	}
}
