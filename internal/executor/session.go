package executor

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
	tap "github.com/amarbel-llc/bob/packages/tap-dancer/go"
)

type SessionExecutor struct {
	Entrypoint  []string
	Description string
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

	tmpDir := filepath.Join(dir, ".tmp")

	// Split session key ("repo/branch") into individual env vars
	repo, branch := key, ""
	if i := strings.Index(key, "/"); i >= 0 {
		repo, branch = key[:i], key[i+1:]
	}

	// Set session env vars so os.ExpandEnv can resolve them in entrypoint args
	sessionEnv := map[string]string{
		"SPINCLASS_SESSION_ID":  key,
		"SPINCLASS_REPO":        repo,
		"SPINCLASS_BRANCH":      branch,
		"SPINCLASS_WORKTREE":    dir,
		"SPINCLASS_DESCRIPTION": s.Description,
		"TMPDIR":                tmpDir,
		"CLAUDE_CODE_TMPDIR":    tmpDir,
	}
	for k, v := range sessionEnv {
		os.Setenv(k, v)
	}

	// Expand env vars in entrypoint args (e.g. "$SPINCLASS_SESSION_ID" → "repo/branch")
	expanded := make([]string, len(entrypoint))
	for i, arg := range entrypoint {
		expanded[i] = os.ExpandEnv(arg)
	}

	if dryRun {
		tp.Skip = "dry run"
		tp.Diagnostics = &tap.Diagnostics{
			Extras: map[string]any{
				"command": strings.Join(expanded, " "),
			},
		}
		return nil
	}

	cmd := exec.Command(expanded[0], expanded[1:]...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
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
			cmd.Process.Signal(syscall.SIGHUP)
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			<-timer.C
			if cmd.Process != nil {
				cmd.Process.Signal(syscall.SIGTERM)
			}
		}
	}()

	err := cmd.Wait()
	signal.Stop(sighup)
	return err
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
