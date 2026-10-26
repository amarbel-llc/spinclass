// Package sessionexec implements `sc exec` — run a utility inside a
// spinclass session's worktree, devshell-scoped via direnv, with the
// session's SPINCLASS_* identity environment set.
//
// Its motivating consumer is the posh "escape to shell" key (FDR-0017
// follow-up): posh intercepts a keystroke above claude's raw-mode tty and
// spawns a configurable command in the session's cwd. Configuring that
// command to `sc exec` lands the user in the worktree root with the devshell
// loaded and the identity env set, rather than a bare shell. `sc exec` is
// also useful standalone — operate on a session's worktree from any terminal.
package sessionexec

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"code.linenisgreat.com/spinclass/internal/alfa/direnv"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// Run resolves a spinclass session and execs util in its worktree,
// devshell-scoped via `direnv exec` (a plain cwd-scoped exec when direnv is
// unavailable), with the SPINCLASS_* identity env set. util defaults to
// $SHELL (then /bin/sh) when empty. The child inherits this process's stdio,
// so an interactive shell takes over the terminal; its exit code is returned
// as an *exec.ExitError for the caller to propagate.
//
// Resolution asymmetry: a non-empty target is resolved via
// session.FindByTarget and a miss (or ambiguity) is an error. An empty
// target auto-detects from cwd via session.FindByWorktreePath; an implicit
// miss DEGRADES — util runs in the current cwd without the SPINCLASS_* env
// (a stderr notice) — so a caller like posh can default its escape command
// to `sc exec` even outside a session.
func Run(target string, util []string) error {
	dir, env, err := resolve(target)
	if err != nil {
		return err
	}
	return execIn(dir, util, env)
}

// resolve maps (target, cwd) to the worktree dir and process env to exec in.
// See Run for the explicit-error / implicit-degrade asymmetry.
func resolve(target string) (dir string, env []string, err error) {
	if target != "" {
		st, ferr := session.FindByTarget(target)
		if ferr != nil {
			return "", nil, ferr // ErrTargetNotFound or ambiguity — surface as-is
		}
		if cerr := checkNotOnceConfined(st); cerr != nil {
			return "", nil, cerr
		}
		return st.WorktreePath, identityEnv(st), nil
	}

	cwd, gerr := os.Getwd()
	if gerr != nil {
		return "", nil, gerr
	}
	if st, ferr := session.FindByWorktreePath(cwd); ferr == nil {
		if cerr := checkNotOnceConfined(st); cerr != nil {
			return "", nil, cerr
		}
		return st.WorktreePath, identityEnv(st), nil
	}

	// Implicit (cwd) miss: degrade to a plain cwd-scoped exec with no
	// SPINCLASS_* identity env. Strip both the inherited clown/claude session
	// ids (#169) AND any inherited SPINCLASS_* — a parent (e.g. an agent
	// session) may carry an identity that does NOT describe this cwd, and
	// leaking it would be misleading. This is what makes the degrade honest:
	// "no session" means no identity env, full stop.
	fmt.Fprintln(os.Stderr,
		"sc exec: not inside a spinclass session — running in the current directory without SPINCLASS_* env")
	return cwd, stripSpinclassEnv(session.StripInheritedSessionIDs(os.Environ())), nil
}

// stripSpinclassEnv drops every SPINCLASS_* entry from env, so a degraded
// (no-session) exec carries no stale identity inherited from a parent.
func stripSpinclassEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "SPINCLASS_") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// IdentityEnv returns the SPINCLASS_* identity environment for st, layered
// over the current process env. It is the exported entry point for callers
// (e.g. internal/foxtrot/run) that build their own *exec.Cmd via CommandIn rather
// than going through Run; identityEnv remains the internal implementation.
func IdentityEnv(st *session.State) []string { return identityEnv(st) }

// identityEnv builds on session.IdentityEnv and session.TmpDirEnv, the one
// var list executor.SessionExecutor and spawn.workerEnv share (#192). It
// strips the launcher's inherited
// CLOWN_SESSION_ID/CLAUDE_SESSION_ID (#169) then appends the spinclass-owned
// SPINCLASS_* vars last so they win on collision (os/exec keeps the last
// occurrence of a duplicated key).
func identityEnv(st *session.State) []string {
	env := session.StripInheritedSessionIDs(os.Environ())
	env = append(env, session.IdentityEnv(st.Key(), st.WorktreePath, st.Description)...)
	return append(env, session.TmpDirEnv(st.WorktreePath)...)
}

// CommandIn builds the *exec.Cmd that runs util in dir with env. An empty
// util defaults to $SHELL (then /bin/sh). When direnv resolves, util is
// wrapped as `direnv exec <dir> <util>...` so the worktree devshell is loaded.
// The caller owns the command's Stdin/Stdout/Stderr — execIn inherits this
// process's stdio (an interactive shell takes over the terminal), while
// callers that capture output (e.g. internal/foxtrot/run, teeing into a crap
// LineWriter) set their own writers.
func CommandIn(dir string, util []string, env []string) *exec.Cmd {
	if len(util) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		util = []string{shell}
	}

	argv := util
	if direnvPath, ok := direnv.Resolve(); ok {
		argv = direnv.WrapExec(direnvPath, dir, util)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd
}

// execIn runs util in dir with env, inheriting this process's stdio so an
// interactive shell takes over the terminal.
func execIn(dir string, util []string, env []string) error {
	cmd := CommandIn(dir, util, env)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ParseArgs splits the passthrough argv of `sc exec` into the optional
// session target and the util argv. Grammar:
//
//	[--session <t> | -s <t> | --session=<t>] [--] [<util> [args...]]
//
// A leading --session/-s (or --session=) sets the target; an optional `--`
// then separates it from the util. Everything else is the util argv (empty
// → $SHELL). The command uses the framework's PassthroughArgs, which bypasses
// flag parsing, so the session flag is parsed here rather than as a Param.
func ParseArgs(args []string) (target string, util []string) {
	i := 0
	if i < len(args) {
		switch a := args[i]; {
		case a == "--session" || a == "-s":
			if i+1 < len(args) {
				target = args[i+1]
				i += 2
			} else {
				i++ // dangling flag with no value — ignore
			}
		case strings.HasPrefix(a, "--session="):
			target = strings.TrimPrefix(a, "--session=")
			i++
		}
	}
	if i < len(args) && args[i] == "--" {
		i++
	}
	return target, args[i:]
}
