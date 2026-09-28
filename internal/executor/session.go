package executor

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
	tap "code.linenisgreat.com/tap/go/pkgs/writer"
	"code.linenisgreat.com/tap/go/pkgs/yaml_diagnostic"
)

type SessionExecutor struct {
	Entrypoint  []string
	Description string
	// Env is user-configured environment variables to inject into the
	// session's process environment, sourced from `[session-entry].env`
	// in the sweatfile cascade. Applied BEFORE spinclass-owned vars so
	// SPINCLASS_SESSION_ID/REPO/BRANCH/WORKTREE/DESCRIPTION and TMPDIR
	// cannot be clobbered. Typical use: set $SPINCLASS_GROUP for a
	// multiplexer so entrypoint argv and liveness probes can reference
	// it symbolically.
	Env map[string]string
}

func (s SessionExecutor) Attach(dir string, key string, command []string, dryRun bool, tp *tap.TestPoint) error {
	entrypoint := s.Entrypoint
	if len(command) > 0 {
		entrypoint = command
	}
	if len(entrypoint) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		entrypoint = []string{shell}
	}

	sessionEnv := s.sessionEnv(dir, key)

	// Expand env vars in entrypoint args (e.g. "$SPINCLASS_SESSION_ID" →
	// "repo/branch") against the session env, falling back to the process env.
	lookup := func(k string) string {
		if v, ok := sessionEnv[k]; ok {
			return v
		}
		return os.Getenv(k)
	}
	expanded := make([]string, len(entrypoint))
	for i, arg := range entrypoint {
		expanded[i] = os.Expand(arg, lookup)
	}

	if dryRun {
		tp.Skip = "dry run"
		tp.Diagnostics = &yaml_diagnostic.YAMLDiagnostic{
			Extras: map[string]any{
				"command": strings.Join(expanded, " "),
			},
		}
		return nil
	}

	cmd := exec.Command(expanded[0], expanded[1:]...)
	cmd.Dir = dir
	// Strip an inherited CLOWN_SESSION_ID/CLAUDE_SESSION_ID (e.g. when `sc` is
	// run from within another clown session) so this session's clown re-derives
	// its channel from the SPINCLASS_SESSION_ID below, rather than arming the
	// launcher's channel (#169). The session env goes ONLY to the child: setting
	// it process-wide leaked this session's TMPDIR into everything spinclass ran
	// afterwards, e.g. post-merge after teardown (spinclass#330).
	cmd.Env = session.StripInheritedSessionIDs(os.Environ())
	for k, v := range sessionEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)

	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		<-sighup
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGHUP) // best-effort forwarding
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			<-timer.C
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
		}
	}()

	err := cmd.Wait()
	signal.Stop(sighup)
	return err
}

// sessionEnv is the environment layered over the process env for the session
// entrypoint: user [session-entry].env first, then the spinclass-owned vars,
// which win on collision because the integration contract requires
// SPINCLASS_SESSION_ID etc. to be authoritative.
func (s SessionExecutor) sessionEnv(dir, key string) map[string]string {
	repo, branch := key, ""
	if i := strings.Index(key, "/"); i >= 0 {
		repo, branch = key[:i], key[i+1:]
	}
	tmpDir := filepath.Join(dir, ".tmp")

	env := make(map[string]string, len(s.Env)+7)
	for k, v := range s.Env {
		env[k] = v
	}
	for k, v := range map[string]string{
		"SPINCLASS_SESSION_ID":  key,
		"SPINCLASS_REPO":        repo,
		"SPINCLASS_BRANCH":      branch,
		"SPINCLASS_WORKTREE":    dir,
		"SPINCLASS_DESCRIPTION": s.Description,
		"TMPDIR":                tmpDir,
		"CLAUDE_CODE_TMPDIR":    tmpDir,
	} {
		env[k] = v
	}
	return env
}

func (s SessionExecutor) Detach() error {
	return nil
}

// RequestClose sends SIGHUP to the PID in the session state file.
func RequestClose(repoPath, branch string) error {
	st, err := session.Read(repoPath, branch)
	if err != nil {
		return nil
	}
	if !session.IsAlive(st.PID) {
		return nil
	}
	return syscall.Kill(st.PID, syscall.SIGHUP)
}
