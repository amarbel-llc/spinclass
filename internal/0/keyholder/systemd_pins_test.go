package keyholder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTool writes an executable shell script named name into dir. It appends
// its argv to log and exits with the code in the file name.exit (default 0),
// printing the contents of name.out when present.
func fakeTool(t *testing.T, dir, name, log string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + log + "'\n" +
		"if [ -f '" + p + ".out' ]; then cat '" + p + ".out'; fi\n" +
		"if [ -f '" + p + ".exit' ]; then exit \"$(cat '" + p + ".exit')\"; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func readLog(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}

func TestSystemdRunnerWithNoPinRefusesAndNeverLooksOnPATH(t *testing.T) {
	poisoned := t.TempDir()
	marker := filepath.Join(t.TempDir(), "poisoned.log")
	fakeTool(t, poisoned, "systemd-run", marker)
	fakeTool(t, poisoned, "systemctl", marker)
	t.Setenv("PATH", poisoned+":"+os.Getenv("PATH"))
	defer OverrideSystemdPins("", "")()

	r := SystemdRunner{}
	ctx := context.Background()
	if err := r.Start(ctx, "u", []string{"/bin/true"}, nil); !errors.Is(err, ErrToolNotPinned) {
		t.Errorf("Start with no pin = %v, want ErrToolNotPinned", err)
	}
	if _, err := r.Active(ctx, "u"); !errors.Is(err, ErrToolNotPinned) {
		t.Errorf("Active with no pin = %v, want ErrToolNotPinned", err)
	}
	if _, err := r.ListUnits(ctx); !errors.Is(err, ErrToolNotPinned) {
		t.Errorf("ListUnits with no pin = %v, want ErrToolNotPinned", err)
	}
	if err := r.Stop(ctx, "u"); !errors.Is(err, ErrToolNotPinned) {
		t.Errorf("Stop with no pin = %v, want ErrToolNotPinned", err)
	}
	if got := readLog(t, marker); got != "" {
		t.Errorf("a program found on PATH ran: %q", got)
	}
}

func TestSystemdRunnerRunsThePinnedPathsNotThePATH(t *testing.T) {
	pinned := t.TempDir()
	pinnedLog := filepath.Join(t.TempDir(), "pinned.log")
	run := fakeTool(t, pinned, "systemd-run", pinnedLog)
	ctl := fakeTool(t, pinned, "systemctl", pinnedLog)
	if err := os.WriteFile(ctl+".out", []byte("active\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	poisoned := t.TempDir()
	poisonedLog := filepath.Join(t.TempDir(), "poisoned.log")
	fakeTool(t, poisoned, "systemd-run", poisonedLog)
	fakeTool(t, poisoned, "systemctl", poisonedLog)
	t.Setenv("PATH", poisoned+":"+os.Getenv("PATH"))
	defer OverrideSystemdPins(run, ctl)()

	r := SystemdRunner{}
	ctx := context.Background()
	if err := r.Start(ctx, "spinclass-keyholder-x", []string{"/nix/store/x/bin/fibby", "--flag"}, []string{"A=b"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if on, err := r.Active(ctx, "spinclass-keyholder-x"); err != nil || !on {
		t.Fatalf("Active = %v, %v, want true", on, err)
	}
	if err := r.Stop(ctx, "spinclass-keyholder-x"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := r.ListUnits(ctx); err != nil {
		t.Fatalf("ListUnits: %v", err)
	}
	got := readLog(t, pinnedLog)
	for _, want := range []string{"--user --quiet --collect --unit=spinclass-keyholder-x", "--setenv=A=b -- /nix/store/x/bin/fibby --flag", "--user is-active spinclass-keyholder-x", "--user stop spinclass-keyholder-x", "list-units"} {
		if !strings.Contains(got, want) {
			t.Errorf("the pinned programs never saw %q; log:\n%s", want, got)
		}
	}
	if p := readLog(t, poisonedLog); p != "" {
		t.Errorf("a program found on PATH ran: %q", p)
	}
}

func TestSystemdRunnerGivenPathsWinOverThePins(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "given.log")
	run := fakeTool(t, dir, "systemd-run", log)
	ctl := fakeTool(t, dir, "systemctl", log)
	defer OverrideSystemdPins("/nonexistent/pinned/systemd-run", "/nonexistent/pinned/systemctl")()
	r := SystemdRunner{RunBin: run, CtlBin: ctl}
	if err := r.Start(context.Background(), "u", []string{"/bin/true"}, nil); err != nil {
		t.Fatalf("Start with an explicit path: %v", err)
	}
	if !strings.Contains(readLog(t, log), "--unit=u") {
		t.Errorf("the explicit systemd-run was not run; log: %q", readLog(t, log))
	}
}

func TestSystemdRunnerRefusesARelativeOrUncleanToolPath(t *testing.T) {
	defer OverrideSystemdPins("", "")()
	for _, p := range []string{"systemd-run", "./systemd-run", "/nix/store/x/../x/bin/systemd-run"} {
		r := SystemdRunner{RunBin: p, CtlBin: "/bin/true"}
		err := r.Start(context.Background(), "u", []string{"/bin/true"}, nil)
		if err == nil || !strings.Contains(err.Error(), "absolute, clean") {
			t.Errorf("RunBin %q: Start = %v, want a refusal naming an absolute, clean path", p, err)
		}
	}
}

func TestActiveTreatsOnlyExit3And4AsNotRunning(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "ctl.log")
	ctl := fakeTool(t, dir, "systemctl", log)
	r := SystemdRunner{CtlBin: ctl}
	for _, c := range []struct {
		exit    string
		wantErr bool
	}{{"3", false}, {"4", false}, {"1", true}, {"2", true}, {"5", true}} {
		if err := os.WriteFile(ctl+".exit", []byte(c.exit), 0o644); err != nil {
			t.Fatal(err)
		}
		on, err := r.Active(context.Background(), "u")
		if on {
			t.Errorf("exit %s: reported running", c.exit)
		}
		if (err != nil) != c.wantErr {
			t.Errorf("exit %s: err = %v, want error = %v", c.exit, err, c.wantErr)
		}
	}
}
