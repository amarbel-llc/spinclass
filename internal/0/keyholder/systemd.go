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
type SystemdRunner struct {
	Timeout time.Duration
}

func (r SystemdRunner) ctx(parent context.Context) (context.Context, context.CancelFunc) {
	d := r.Timeout
	if d <= 0 {
		d = 10 * time.Second
	}
	return context.WithTimeout(parent, d)
}

// Start runs argv as a transient service. --collect removes the unit when it
// stops, so a stopped pair leaves no failed unit behind; Restart stays off.
func (r SystemdRunner) Start(ctx context.Context, unit string, argv, env []string) error {
	ctx, cancel := r.ctx(ctx)
	defer cancel()
	args := []string{"--user", "--quiet", "--collect", "--unit=" + unit, "--property=KillMode=control-group"}
	for _, e := range env {
		args = append(args, "--setenv="+e)
	}
	args = append(args, "--")
	args = append(args, argv...)
	out, err := exec.CommandContext(ctx, "systemd-run", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd-run %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Active reports whether the unit is active (exit status 0 of is-active).
func (r SystemdRunner) Active(ctx context.Context, unit string) (bool, error) {
	ctx, cancel := r.ctx(ctx)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "is-active", unit).Output()
	if err == nil {
		return strings.TrimSpace(string(out)) == "active", nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		// 3 is "inactive", 4 is "no such unit": both mean not running.
		return false, nil
	}
	return false, fmt.Errorf("systemctl is-active %s: %w", unit, err)
}

// Stop stops the unit. A unit that is not loaded is already stopped.
func (r SystemdRunner) Stop(ctx context.Context, unit string) error {
	ctx, cancel := r.ctx(ctx)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "stop", unit).CombinedOutput()
	if err != nil && !bytes.Contains(out, []byte("not loaded")) {
		return fmt.Errorf("systemctl stop %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
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
