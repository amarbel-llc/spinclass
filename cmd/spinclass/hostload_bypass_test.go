package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/hostqueue"
)

func TestIsServeCommand(t *testing.T) {
	for args, want := range map[string]bool{
		"/nix/store/x/bin/spinclass serve":          true,
		"sc serve":                                  true,
		"/nix/store/x/bin/.spinclass-wrapped serve": true,
		"/nix/store/x/bin/spinclass list":           false,
		"/usr/bin/vim serve":                        false,
		"spinclass":                                 false,
	} {
		if got := isServeCommand(strings.Fields(args)); got != want {
			t.Errorf("isServeCommand(%q) = %v, want %v", args, got, want)
		}
	}
}

func TestBinaryHasQueueFindsTheMarkerAcrossChunkBoundaries(t *testing.T) {
	dir := t.TempDir()
	with := filepath.Join(dir, "with")
	pad := strings.Repeat("x", 1<<20-5) // the marker straddles the 1 MiB read
	if err := os.WriteFile(with, []byte(pad+hostqueue.AdmissionMarker+"tail"), 0o600); err != nil {
		t.Fatal(err)
	}
	without := filepath.Join(dir, "without")
	if err := os.WriteFile(without, []byte(pad+"gate slots"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := binaryHasQueue(with); err != nil || !ok {
		t.Errorf("with marker: ok=%v err=%v", ok, err)
	}
	if ok, err := binaryHasQueue(without); err != nil || ok {
		t.Errorf("without marker: ok=%v err=%v", ok, err)
	}
	if _, err := binaryHasQueue(filepath.Join(dir, "absent")); err == nil {
		t.Error("an unreadable binary must error")
	}
}

func TestQueueBypassesNamesOnlyServesWithoutTheQueueCode(t *testing.T) {
	dir := t.TempDir()
	old, cur := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	if err := os.WriteFile(old, []byte("no queue here"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cur, []byte("1 of 1 "+hostqueue.AdmissionMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := liveServeProcesses
	t.Cleanup(func() { liveServeProcesses = prev })
	liveServeProcesses = func() []serveProcess {
		return []serveProcess{
			{pid: 3, session: "a/old", exe: "/store/old", binary: old},
			{pid: 1, session: "b/new", exe: "/store/new", binary: cur},
			{pid: 2, session: "c/old", exe: "/store/old", binary: old},
			{pid: 4, session: "d/gone", exe: "/store/gone", binary: filepath.Join(dir, "absent")},
		}
	}
	got := queueBypasses()
	if len(got) != 2 || got[0].PID != 2 || got[1].PID != 3 {
		t.Fatalf("bypasses = %+v, want pids 2 and 3 only", got)
	}
	out := formatQueueBypass(got)
	if !strings.Contains(out, "QUEUE BYPASS: 2") || !strings.Contains(out, "c/old (pid 2)") {
		t.Errorf("format = %q", out)
	}
	if formatQueueBypass(nil) != "" {
		t.Error("no bypass must render nothing")
	}
}
