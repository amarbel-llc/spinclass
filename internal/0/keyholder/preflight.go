package keyholder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// What a pair really needs, checked without starting one. `sc validate`,
// `sc keyholder-selftest` and a spawn (before it creates anything) all ask
// Launcher.Preflight, so they cannot disagree. The two programs a pair runs,
// fibby and piggy, are the caller's: they come from trusted settings, which
// this package does not read.

// Problem is one precondition that does not hold: what is wrong, and what the
// person does next.
type Problem struct {
	Name string `json:"name"`
	What string `json:"what"`
	Next string `json:"next"`
}

func (p Problem) String() string { return p.Name + ": " + p.What + "\n    next: " + p.Next }

// preflighter is a Runner that has preconditions of its own: the real one has,
// a test's fake has none.
type preflighter interface {
	Preflight(ctx context.Context) []Problem
}

// Preflight returns every precondition of a start that does not hold, or nil.
func (l *Launcher) Preflight(ctx context.Context) []Problem {
	var out []Problem
	// The socket paths depend on the base only: every session's hash is as long.
	if err := l.checkSocketPaths(l.layoutFor("preflight")); err != nil {
		out = append(out, Problem{
			Name: "state-dir", What: err.Error(),
			Next: "set XDG_STATE_HOME to a shorter absolute directory and try again",
		})
	}
	if p, ok := l.Run.(preflighter); ok {
		out = append(out, p.Preflight(ctx)...)
		// The two programs a pair runs, when the caller named them: where they
		// may come from is the caller's rule, that they can be run is everyone's.
		for _, t := range []struct{ name, path string }{{"fibby", l.Tools.Fibby}, {"piggy", l.Tools.Piggy}} {
			if t.path == "" {
				continue
			}
			if err := checkExecutable(t.path); err != nil {
				out = append(out, Problem{
					Name: t.name, What: fmt.Sprintf("the %s program %s cannot be run: %v", t.name, t.path, err),
					Next: "name a program that is there: [keyholder] " + t.name + " in a sweatfile above the repo, or --" + t.name + " for the self-test",
				})
			}
		}
	}
	return out
}

func (l *Launcher) checkSocketPaths(ly layout) error {
	for _, p := range []string{ly.ctl, ly.pcsc, ly.agent} {
		if len(p) > MaxSocketPath {
			return fmt.Errorf("keyholder: socket path %q is %d bytes, over the %d-byte limit: set a shorter XDG_STATE_HOME", p, len(p), MaxSocketPath)
		}
	}
	return nil
}

// probeUnit is a unit nothing creates: asking systemd whether it is active is
// the same call a start makes, and answers only if the user manager does.
const probeUnit = "spinclass-keyholder-preflight.service"

// Preflight checks the four pinned tools and that the user manager answers.
func (r SystemdRunner) Preflight(ctx context.Context) []Problem {
	var out []Problem
	systemctlOK := true
	for _, t := range []struct {
		name string
		path func() (string, error)
	}{
		{"systemd-run", r.runBin},
		{"systemctl", r.ctlBin},
		{"ssh-add", SSHAddPin},
		{"ssh-keygen", SSHKeygenPin},
	} {
		path, err := t.path()
		if err != nil {
			out = append(out, Problem{
				Name: t.name, What: "this build of spinclass has no built-in path for " + t.name,
				Next: "run the packaged spinclass (the host's package, or `nix build .#default` in a spinclass checkout); a plain `go build` cannot run the key holder",
			})
		} else if err := checkExecutable(path); err != nil {
			out = append(out, Problem{
				Name: t.name, What: fmt.Sprintf("the built-in path %s cannot be run: %v", path, err),
				Next: "reinstall or rebuild spinclass (the store path may have been garbage-collected)",
			})
		} else {
			continue
		}
		if t.name == "systemctl" {
			systemctlOK = false
		}
	}
	if systemctlOK {
		if _, err := r.Active(ctx, probeUnit); err != nil {
			out = append(out, Problem{
				Name: "user-manager", What: "your systemd user manager does not answer: " + strings.TrimSpace(err.Error()),
				Next: "run `loginctl enable-linger \"$USER\"`, then run spinclass from a shell where XDG_RUNTIME_DIR is set (check with `systemctl --user is-system-running`)",
			})
		}
	}
	return out
}

func checkExecutable(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return errors.New("not an executable file")
	}
	return nil
}

// PreflightError is the refusal a person sees when problems is not empty.
func PreflightError(problems []Problem) error {
	if len(problems) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("the key holder is on but cannot start:")
	for _, p := range problems {
		b.WriteString("\n  " + p.String())
	}
	b.WriteString("\nafter fixing it, check with `sc keyholder-selftest`; to run without the key holder: `sc keyholder-switch off`")
	return errors.New(b.String())
}
