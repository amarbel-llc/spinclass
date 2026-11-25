package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/0/sessionlog"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
)

// fibbyBin and piggyBin are the absolute store paths of the two programs the
// key holder pair runs, burnt in at link time by a build that pins them
// (-X main.fibbyBin=..., -X main.piggyBin=...). Unpinned they are empty and the
// launcher REFUSES to start a pair: it never looks either up on PATH, because
// the pair holds a session's key and runs outside the unit, and a name resolved
// from a PATH a session can influence is the hole.
var (
	fibbyBin = ""
	piggyBin = ""
)

func init() {
	spawn.KeyholderEnv = func(home, repoPath, sessionKey string) (map[string]string, error) {
		return keyholderSessionEnv(home, repoPath, sessionKey, os.Stderr)
	}
}

// keyholderLauncherFn builds the launcher for the real system; tests replace it.
var keyholderLauncherFn = realKeyholderLauncher

// realKeyholderLauncher requires both programs to be pinned into this build as
// absolute paths and refuses, naming the reason, when either is not.
func realKeyholderLauncher() (*keyholder.Launcher, error) {
	return launcherForTools(fibbyBin, piggyBin)
}

// launcherForTools builds the real-system launcher for two absolute program
// paths. The pins of a build come through realKeyholderLauncher; the hidden
// `sc keyholder-pair` may name its own, because a person at a terminal chose
// them. A session launch never does.
func launcherForTools(fibby, piggy string) (*keyholder.Launcher, error) {
	for _, p := range []struct{ name, path string }{{"fibby", fibby}, {"piggy", piggy}} {
		if p.path == "" {
			return nil, fmt.Errorf("the key holder is on but %s is not pinned into this build (-X main.%sBin): a pair is never started from a name looked up on PATH", p.name, p.name)
		}
		if !filepath.IsAbs(p.path) {
			return nil, fmt.Errorf("the key holder is on but the pinned %s %q is not an absolute path", p.name, p.path)
		}
	}
	return &keyholder.Launcher{
		Base: keyholder.StateBase(), Tools: keyholder.Tools{Fibby: fibby, Piggy: piggy}, Run: keyholder.SystemdRunner{},
		Log: func(format string, args ...any) { sessionlog.Infof(format, args...) },
	}, nil
}

// keyholderSessionEnv is the key holder's one entry point for a session launch:
// with [keyholder] mode on (a trusted layer's word; a repo or worktree layer's
// is ignored and reported) it makes sure the session's pair is running and
// returns the environment that points the session at the holder's socket in
// place of the operator's agent. With the mode off it returns nil and starts
// nothing. A failure is an error and never a fall back to the operator's agent:
// a session that was meant to run on its own key does not quietly run on his.
func keyholderSessionEnv(home, repoPath, sessionKey string, stderr io.Writer) (map[string]string, error) {
	h, err := sweatfileio.LoadHierarchy(home, repoPath)
	if err != nil {
		return nil, fmt.Errorf("reading the sweatfiles for [keyholder]: %w", err)
	}
	on, untrusted, err := sweatfileio.TrustedKeyholderMode(h, home, repoPath)
	for _, path := range untrusted {
		fmt.Fprintf(stderr, "spinclass: ignoring [keyholder] in %s: only layers above the repo may set it\n", path)
	}
	if err != nil {
		return nil, fmt.Errorf("%w (sessions are not launched until it is fixed: a wrong key holder switch must not fall back to the operator's agent)", err)
	}
	if !on {
		return nil, nil
	}
	l, err := keyholderLauncherFn()
	if err != nil {
		return nil, err
	}
	pair, err := l.Start(context.Background(), sessionKey)
	if err != nil {
		return nil, fmt.Errorf("starting the session's key holder: %w", err)
	}
	return map[string]string{"SSH_AUTH_SOCK": pair.AgentSock}, nil
}

// withKeyholderEnv returns env with the key holder's variables laid over it
// (they replace the operator's SSH_AUTH_SOCK); env itself is never modified.
func withKeyholderEnv(env, kh map[string]string) map[string]string {
	if len(kh) == 0 {
		return env
	}
	out := make(map[string]string, len(env)+len(kh))
	maps.Copy(out, env)
	maps.Copy(out, kh)
	return out
}
