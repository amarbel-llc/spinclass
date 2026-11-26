package keyholder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// SystemdRunner runs units with `systemd-run --user` and asks about them with
// `systemctl --user`. Every call is bounded by Timeout (ten seconds if zero).
// The two programs are the absolute paths in RunBin and CtlBin when a person at
// a terminal named them, else the build's pins (SetSystemdPins); never a name
// looked up on PATH (see systempins.go).
type SystemdRunner struct {
	Timeout time.Duration
	RunBin  string
	CtlBin  string
}

func (r SystemdRunner) ctx(parent context.Context) (context.Context, context.CancelFunc) {
	d := r.Timeout
	if d <= 0 {
		d = 10 * time.Second
	}
	return context.WithTimeout(parent, d)
}

// Start runs argv as a transient service. --collect removes the unit when it
// stops, so a stopped pair leaves no failed unit behind; Restart stays off. Each
// of props (`BindsTo=unit`) is passed as a --property.
func (r SystemdRunner) Start(ctx context.Context, unit string, argv, env []string, props ...string) error {
	bin, err := r.runBin()
	if err != nil {
		return err
	}
	ctx, cancel := r.ctx(ctx)
	defer cancel()
	args := []string{"--user", "--quiet", "--collect", "--unit=" + unit, "--property=KillMode=control-group"}
	for _, p := range props {
		args = append(args, "--property="+p)
	}
	for _, e := range env {
		args = append(args, "--setenv="+e)
	}
	args = append(args, "--")
	args = append(args, argv...)
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd-run %s: %w: %s%s", unit, err, strings.TrimSpace(string(out)), userManagerHint(string(out)))
	}
	return nil
}

// userManagerHint is the sentence that names lingering when a systemd call failed
// because there is no user manager to talk to (the pair runs as user units, which
// need one that outlives logins), and "" for any other failure. It matches the
// words systemd itself prints when it cannot reach the user bus.
func userManagerHint(output string) string {
	for _, m := range []string{"Failed to connect to", "No medium found", "Connection refused", "XDG_RUNTIME_DIR"} {
		if strings.Contains(output, m) {
			return " (the systemd user manager of this account is not reachable: the key holder runs as user units, which need one that outlives logins; enable lingering with `loginctl enable-linger <user>` and run spinclass where XDG_RUNTIME_DIR is set)"
		}
	}
	return ""
}

// Active reports whether the unit is active (exit status 0 of is-active). Only
// exit 3 (inactive) and 4 (no such unit) mean "not running"; any other failure,
// such as no user bus, is an error and is never read as a stopped pair.
func (r SystemdRunner) Active(ctx context.Context, unit string) (bool, error) {
	bin, err := r.ctlBin()
	if err != nil {
		return false, err
	}
	ctx, cancel := r.ctx(ctx)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--user", "is-active", unit).Output()
	if err == nil {
		return strings.TrimSpace(string(out)) == "active", nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && (ee.ExitCode() == 3 || ee.ExitCode() == 4) {
		return false, nil
	}
	hint := ""
	if ee != nil {
		hint = userManagerHint(string(ee.Stderr))
	}
	return false, fmt.Errorf("systemctl is-active %s: %w%s", unit, err, hint)
}

// ListUnits returns the loaded key holder units, in any state, by name.
func (r SystemdRunner) ListUnits(ctx context.Context) ([]string, error) {
	bin, err := r.ctlBin()
	if err != nil {
		return nil, err
	}
	ctx, cancel := r.ctx(ctx)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--user", "list-units", "--all", "--plain", "--no-legend", "--no-pager", "spinclass-keyholder-*").Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w", err)
	}
	var units []string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.HasSuffix(f[0], ".service") {
			units = append(units, f[0])
		}
	}
	return units, nil
}

// Stop stops the unit. A unit that is not loaded is already stopped.
func (r SystemdRunner) Stop(ctx context.Context, unit string) error {
	bin, err := r.ctlBin()
	if err != nil {
		return err
	}
	ctx, cancel := r.ctx(ctx)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--user", "stop", unit).CombinedOutput()
	if err != nil && !bytes.Contains(out, []byte("not loaded")) {
		return fmt.Errorf("systemctl stop %s: %w: %s%s", unit, err, strings.TrimSpace(string(out)), userManagerHint(string(out)))
	}
	return nil
}

// Output runs argv to completion with env added to a minimal inherited
// environment (PATH and HOME only, so the operator's agent is never inherited).
func (r SystemdRunner) Output(ctx context.Context, argv, env []string) ([]byte, error) {
	ctx, cancel := r.ctx(ctx)
	defer cancel()
	if len(argv) == 0 {
		return nil, errors.New("keyholder: empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(minimalEnv(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", argv[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
