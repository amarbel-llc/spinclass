package keyholder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func problemNames(ps []Problem) string {
	var n []string
	for _, p := range ps {
		n = append(n, p.Name)
		if p.What == "" || p.Next == "" {
			n = append(n, "(a problem without what or next)")
		}
	}
	return strings.Join(n, " ")
}

// preflightTools writes four stand-ins; systemctl behaves as body says.
func preflightTools(t *testing.T, systemctl string) {
	t.Helper()
	dir := t.TempDir()
	tool := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Cleanup(OverrideSystemdPins(tool("systemd-run", "exit 0"), tool("systemctl", systemctl)))
	t.Cleanup(OverrideSSHPins(tool("ssh-add", "exit 0"), tool("ssh-keygen", "exit 0")))
}

func realLauncher(base string) *Launcher { return &Launcher{Base: base, Run: SystemdRunner{}} }

func TestPreflightNamesEveryUnpinnedTool(t *testing.T) {
	t.Cleanup(OverrideSystemdPins("", ""))
	t.Cleanup(OverrideSSHPins("", ""))
	got := problemNames(realLauncher("/s").Preflight(context.Background()))
	if got != "systemd-run systemctl ssh-add ssh-keygen" {
		t.Errorf("problems %q", got)
	}
}

func TestPreflightPassesWithEveryToolAndAnAnsweringUserManager(t *testing.T) {
	// is-active of a unit nothing created: systemd exits 3.
	preflightTools(t, "exit 3")
	if ps := realLauncher("/s").Preflight(context.Background()); len(ps) != 0 {
		t.Errorf("problems %v", ps)
	}
}

func TestPreflightReportsAUserManagerThatDoesNotAnswer(t *testing.T) {
	preflightTools(t, "echo 'Failed to connect to bus: No medium found' >&2; exit 1")
	ps := realLauncher("/s").Preflight(context.Background())
	if problemNames(ps) != "user-manager" || !strings.Contains(ps[0].Next, "enable-linger") {
		t.Errorf("problems %v", ps)
	}
}

func TestPreflightReportsAPinThatIsNotThereAndALongStateDir(t *testing.T) {
	preflightTools(t, "exit 3")
	t.Cleanup(OverrideSSHPins("/nonexistent/ssh-add", filepath.Join(t.TempDir())))
	got := problemNames(realLauncher("/" + strings.Repeat("x", 120)).Preflight(context.Background()))
	if got != "state-dir ssh-add ssh-keygen" {
		t.Errorf("problems %q", got)
	}
}

func TestPreflightReportsAProgramThatCannotBeRun(t *testing.T) {
	preflightTools(t, "exit 3")
	l := realLauncher("/s")
	l.Tools = Tools{Fibby: "/nonexistent/fibby", Piggy: "/bin/sh"}
	if got := problemNames(l.Preflight(context.Background())); got != "fibby" {
		t.Errorf("problems %q", got)
	}
}

func TestPreflightErrorSaysWhatToDo(t *testing.T) {
	if PreflightError(nil) != nil {
		t.Error("no problems must be no error")
	}
	err := PreflightError([]Problem{{Name: "ssh-add", What: "gone", Next: "rebuild"}})
	for _, want := range []string{"cannot start", "ssh-add: gone", "next: rebuild", "sc keyholder-selftest", "sc keyholder-switch off"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}
