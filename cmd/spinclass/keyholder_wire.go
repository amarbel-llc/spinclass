package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/0/sessionlog"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/validate"
	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
)

func init() {
	spawn.KeyholderEnv = func(home, repoPath, sessionKey string) (map[string]string, func(), error) {
		return keyholderSessionEnvUndo(home, repoPath, sessionKey, os.Stderr)
	}
	spawn.KeyholderPreflight = keyholderPreflight
}

// keyholderPreflight says whether a worker's pair could start: nil with the
// switch off, else every precondition that does not hold.
func keyholderPreflight(home, repoPath, _ string) error {
	sw, err := resolveKeyholderSwitch(home, repoPath)
	if err != nil || !sw.On {
		return err
	}
	l, err := keyholderLauncherFn(keyholder.Tools{Fibby: sw.Fibby, Piggy: sw.Piggy})
	if err != nil {
		return err
	}
	return keyholder.PreflightError(l.Preflight(context.Background()))
}

// keyholderLauncherFn builds the launcher for the real system from the two
// program paths the trusted [keyholder] setting names; tests replace it.
var keyholderLauncherFn = realKeyholderLauncher

// realKeyholderLauncher is the launcher a session launch uses. The programs come
// ONLY from the trusted [keyholder] fibby and piggy settings, which the host's
// configuration writes from the one piggy the host runs as its agent: spinclass
// pins no piggy of its own and never looks either program up on PATH, because
// the pair holds a session's key and runs outside the unit, and a name resolved
// from a PATH a session can influence is the hole. Either path unset or not a
// store program (CheckKeyholderToolPath) refuses, naming the setting.
func realKeyholderLauncher(tools keyholder.Tools) (*keyholder.Launcher, error) {
	var problems []keyholder.Problem
	for _, p := range []struct{ name, path string }{{"fibby", tools.Fibby}, {"piggy", tools.Piggy}} {
		if err := sweatfileio.CheckKeyholderToolPath(p.name, p.path); err != nil {
			problems = append(problems, keyholder.Problem{
				Name: p.name, What: err.Error() + ": a pair is never started from a name looked up on PATH",
				Next: validate.KeyholderProgramNext,
			})
		}
	}
	if problems != nil {
		return nil, &keyholderToolsError{problems}
	}
	return newKeyholderLauncher(tools), nil
}

// keyholderToolsError is a [keyholder] fibby or piggy that cannot be used. A
// launch (the switch is on) reads it as the same refusal every precondition
// gives; the self-test, which runs with the mode off too, takes the problems.
type keyholderToolsError struct{ problems []keyholder.Problem }

func (e *keyholderToolsError) Error() string { return keyholder.PreflightError(e.problems).Error() }

// launcherForTools builds the real-system launcher for two absolute program
// paths a person at a terminal named: the hidden `sc keyholder-pair` takes
// them from flags because that person chose them. A session launch never does
// (it goes through realKeyholderLauncher and the trusted settings).
func launcherForTools(fibby, piggy string) (*keyholder.Launcher, error) {
	return launcherForToolsAndSystemd(fibby, piggy, "", "")
}

// launcherForToolsAndSystemd is launcherForTools plus the absolute paths of
// systemd-run and systemctl the same person chose, for a build that has no pins
// for them (a dev build): an empty value falls back to the build's pin and, with
// no pin either, the runner refuses. Never to a name looked up on PATH.
func launcherForToolsAndSystemd(fibby, piggy, systemdRun, systemctl string) (*keyholder.Launcher, error) {
	for _, p := range []struct{ name, path string }{{"fibby", fibby}, {"piggy", piggy}} {
		if p.path == "" {
			return nil, fmt.Errorf("%s is not named: pass --fibby and --piggy (absolute paths); a session launch reads [keyholder] fibby and piggy instead, and a pair is never started from a name looked up on PATH", p.name)
		}
		if !filepath.IsAbs(p.path) {
			return nil, fmt.Errorf("the %s %q is not an absolute path", p.name, p.path)
		}
	}
	for _, p := range []struct{ name, path string }{{"systemd-run", systemdRun}, {"systemctl", systemctl}} {
		if p.path != "" && !filepath.IsAbs(p.path) {
			return nil, fmt.Errorf("the %s %q is not an absolute path", p.name, p.path)
		}
	}
	l := newKeyholderLauncher(keyholder.Tools{Fibby: fibby, Piggy: piggy})
	l.Run = keyholder.SystemdRunner{RunBin: systemdRun, CtlBin: systemctl}
	return l, nil
}

func newKeyholderLauncher(tools keyholder.Tools) *keyholder.Launcher {
	return &keyholder.Launcher{
		Base: keyholder.StateBase(), Tools: tools, Run: keyholder.SystemdRunner{},
		Log: func(format string, args ...any) { sessionlog.Infof(format, args...) },
	}
}

// keyholderSessionEnv is the key holder's one entry point for a session launch:
// with [keyholder] mode on (a trusted layer's word; a repo or worktree layer's
// is ignored and reported) it makes sure the session's pair is running and
// returns the environment that points the session at the holder's socket in
// place of the operator's agent. With the mode off it returns nil and starts
// nothing. A failure is an error and never a fall back to the operator's agent:
// a session that was meant to run on its own key does not quietly run on his.
func keyholderSessionEnv(home, repoPath, sessionKey string, stderr io.Writer) (map[string]string, error) {
	kh, _, err := keyholderSessionEnvUndo(home, repoPath, sessionKey, stderr)
	return kh, err
}

// keyholderSessionEnvUndo is keyholderSessionEnv plus an undo: non-nil only when
// THIS call launched the session's pair (an existing live pair is left alone and
// has nothing to undo). A launch that fails after the pair started, and so leaves
// no session to close it, calls undo to stop the pair.
func keyholderSessionEnvUndo(home, repoPath, sessionKey string, stderr io.Writer) (map[string]string, func(), error) {
	sw, err := resolveKeyholderSwitch(home, repoPath)
	for _, path := range sw.Untrusted {
		fmt.Fprintf(stderr, "spinclass: ignoring [keyholder] in %s: only layers above the repo may set it\n", path)
	}
	if err != nil {
		return nil, nil, err
	}
	if !sw.On {
		return nil, nil, nil
	}
	l, err := keyholderLauncherFn(keyholder.Tools{Fibby: sw.Fibby, Piggy: sw.Piggy})
	if err != nil {
		return nil, nil, err
	}
	if err := keyholder.PreflightError(l.Preflight(context.Background())); err != nil {
		return nil, nil, err
	}
	pair, err := l.Start(context.Background(), sessionKey)
	if err != nil {
		return nil, nil, fmt.Errorf("starting the session's key holder: %w\n    next: run `sc keyholder-selftest` to see the failing step, or run without the key holder: `sc keyholder-switch off`", err)
	}
	if pair.Reminted != nil {
		// Every launch, resume, spawn, start hand-off and restart comes through
		// here, so a lost pair is announced once, in one place.
		announceKeyholderRemint(l, sessionKey, pair.Reminted, stderr)
	}
	// The certificate path is part of the session's environment beside its agent:
	// where a signer installs a certificate and where the session reads it.
	kh := map[string]string{"SSH_AUTH_SOCK": pair.AgentSock, keyholder.CertEnv: keyholder.CertPathIn(pair.Dir)}
	// Every other variable that names the operator's agent, set in the launching
	// process, is emptied for the session (empty is unset to piggy and to ssh).
	for _, name := range keyholder.OperatorAgentVarsIn(os.Environ()) {
		kh[name] = ""
	}
	var undo func()
	if pair.Created {
		undo = func() {
			if err := l.Stop(context.Background(), sessionKey); err != nil {
				fmt.Fprintf(stderr, "spinclass: stopping the key holder pair of %s after a failed launch: %v\n", sessionKey, err)
			}
		}
	}
	// A plain commit in the session signs with the holder's key: git settings in
	// its environment (keyholder.GitSigningEnv).
	signing, err := holderGitSigningEnv(l, repoPath, sessionKey, pair)
	if err != nil {
		if undo != nil {
			undo()
		}
		return nil, nil, fmt.Errorf("setting up commit signing with the session's key holder: %w\n    next: run `sc keyholder-selftest` to see the failing step, or run without the key holder: `sc keyholder-switch off`", err)
	}
	maps.Copy(kh, signing)
	return kh, undo, nil
}

func holderGitSigningEnv(l *keyholder.Launcher, repoPath, sessionKey string, pair keyholder.Pair) (map[string]string, error) {
	keygen, err := keyholder.SSHKeygenPin()
	if err != nil {
		return nil, err
	}
	return keyholder.GitSigningEnv(l.Base, sessionKey, keygen, pair, os.Environ(), configuredAllowedSigners(repoPath))
}

// maxAllowedSignersSize bounds the operator's allowed-signers file copied into
// a session's.
const maxAllowedSignersSize = 1 << 20

// configuredAllowedSigners is the content of the allowed-signers file git is
// already configured with for repoPath, so the session still verifies those
// signatures; nil when none is named or it cannot be read.
func configuredAllowedSigners(repoPath string) []byte {
	path, err := git.Run(repoPath, "config", "--type=path", "--get", "gpg.ssh.allowedSignersFile")
	if err != nil || path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoPath, path)
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || info.Size() > maxAllowedSignersSize {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}

// withKeyholderEnv returns env with the key holder's variables laid over it
// (they replace the operator's SSH_AUTH_SOCK) and every other variable that
// names the operator's agent emptied, including one a configured entry env set;
// env itself is never modified.
func withKeyholderEnv(env, kh map[string]string) map[string]string {
	if len(kh) == 0 {
		return env
	}
	out := make(map[string]string, len(env)+len(kh))
	maps.Copy(out, env)
	for k := range out {
		if keyholder.IsOperatorAgentVar(k) {
			out[k] = ""
		}
	}
	maps.Copy(out, kh)
	return out
}
