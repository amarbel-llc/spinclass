package hookrun

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/clown"
	"code.linenisgreat.com/spinclass/internal/0/embeds"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// The guarded gate (spinclass#449). The repo's [hooks].pre-merge runs the
// branch's own tree in the frontend: `just` from the branch's justfile, inside
// the session worktree's devshell (`direnv exec`, which reads `.envrc`, the
// flake as the worktree holds it, and `dotenv .spinclass/env`), so a worker
// whose session is confined can put code of its own where the gate executes it
// with the operator's authority. No set of files can be the control for that.
// A guarded merge therefore runs a command from a trusted sweatfile layer
// instead, in the detached build worktree at the landing sha, with no direnv,
// an environment built from scratch, and the nix evaluator's escape hatches
// pinned shut, so that the only code the gate executes is what nix runs inside
// its sandbox. What the worker can still reach is the nix evaluator itself,
// which is trusted here the way the daemon is.

// ConfinedGateNixConfig is the NIX_CONFIG a guarded gate runs under, applied
// after the operator's nix.conf and before the command's own --option flags.
// allow-unsafe-native-code-during-evaluation and accept-flake-config are
// evaluator settings and always hold. sandbox is decided by the daemon: for a
// client that is not a trusted-user the daemon keeps its own nix.conf value,
// so the pin holds only where the operator is trusted or the daemon already
// sandboxes, which spinclass-sweatfile(5) says. A trusted command that passes
// `--impure` or `--option sandbox false` is the operator's choice.
const ConfinedGateNixConfig = "allow-unsafe-native-code-during-evaluation = false\n" +
	"accept-flake-config = false\n" +
	"sandbox = true\n"

// confinedGateTools are the programs a guarded gate's PATH is built from. A
// build made by lib.mkSpinclass with the gate pins pins each at link time
// (embeds.GateTool, spinclass#454, the way direnv and madder are pinned), so
// the frontend's PATH is never consulted for it; an unpinned tool (a plain
// `go build`, or a consumer that pins only some) is resolved from the
// frontend's PATH when the gate starts. Either way ConfinedGateEnv refuses a
// tool that resolves under a directory the worker writes, and a frontend that
// itself runs inside such a directory's direnv.
var confinedGateTools = []string{"nix", "git", "sh"}

// SetConfinedGateTools replaces the tool list for a test (a sandbox without
// nix) and returns the restore.
func SetConfinedGateTools(tools ...string) (restore func()) {
	prev := confinedGateTools
	confinedGateTools = tools
	return func() { confinedGateTools = prev }
}

// confinedGateKeep are the inherited variables a guarded gate still sees: who
// the operator is, how nix reaches its daemon and TLS roots, and how
// systemd-run reaches the user manager for the hook's scope. Everything else
// (SSH_AUTH_SOCK, GIT_*, NIX_CONFIG, NIX_PATH, BASH_ENV, ENV, DIRENV_*,
// TMPDIR, CLOWN_*, SPINCLASS_*, LD_*) is dropped.
var confinedGateKeep = []string{
	"HOME", "USER", "LOGNAME", "LANG", "LC_ALL",
	"NIX_REMOTE", "NIX_SSL_CERT_FILE", "SSL_CERT_FILE",
	"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS",
}

// ConfinedGateEnv builds a guarded gate's whole environment for a gate run in
// runDir: a PATH of the frontend's tool directories only, the kept variables,
// the pinned NIX_CONFIG, GIT_ALLOW_PROTOCOL limited to file and https (a
// flake input is fetched by nix, never by an ssh helper the worker could
// steer), WORKTREE naming the build worktree, and SPINCLASS_GUARDED_GATE=1 so
// a command can tell. forbidden are directories the worker writes (the
// session worktree, the repo's .worktrees): a tool that resolves under one,
// or a frontend whose own direnv (DIRENV_DIR, DIRENV_FILE) points into one,
// is an error, as is a tool not on the frontend's PATH. The gate then fails
// closed before anything runs.
func ConfinedGateEnv(runDir string, forbidden []string) ([]string, error) {
	g, err := newConfinedGate(runDir, forbidden)
	if err != nil {
		return nil, err
	}
	return g.env, nil
}

// confinedGate is what a guarded gate runs with: its environment, the
// absolute shell to exec (never a PATH lookup at exec time, which would be a
// second lookup on the frontend's PATH after the check), and the directories
// the worker writes, which the scope prefix is checked against too.
type confinedGate struct {
	env       []string
	sh        string
	forbidden []string
}

func newConfinedGate(runDir string, forbidden []string) (*confinedGate, error) {
	for _, v := range []string{"DIRENV_DIR", "DIRENV_FILE"} {
		p := strings.TrimPrefix(os.Getenv(v), "-")
		if d := underAny(p, forbidden); d != "" {
			return nil, fmt.Errorf("guarded gate: the frontend runs inside the direnv of %s (%s=%s), so its PATH is that worktree's devshell; start the merge from outside it", d, v, os.Getenv(v))
		}
	}
	// Any PATH entry the worker writes poisons every lookup, so the whole
	// PATH is checked, not only the tools found on it.
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if d := underAny(entry, forbidden); d != "" {
			return nil, fmt.Errorf("guarded gate: the frontend's PATH has %s, under %s, which the worker writes; start the merge from a shell without it", entry, d)
		}
	}
	var dirs []string
	seen := map[string]bool{}
	resolvedTool := map[string]string{}
	for _, tool := range confinedGateTools {
		p, err := resolveGateTool(tool, forbidden)
		if err != nil {
			return nil, err
		}
		resolvedTool[tool] = p
		if d := filepath.Dir(p); !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	sh, ok := resolvedTool["sh"]
	if !ok {
		// A test's tool list without sh: still exec an absolute shell.
		var err error
		if sh, err = resolveGateTool("sh", forbidden); err != nil {
			return nil, err
		}
	}
	env := []string{"PATH=" + strings.Join(dirs, ":")}
	for _, k := range confinedGateKeep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env,
		"NIX_CONFIG="+ConfinedGateNixConfig,
		"GIT_ALLOW_PROTOCOL=file:https",
		"WORKTREE="+runDir,
		"SPINCLASS_GUARDED_GATE=1",
	)
	return &confinedGate{env: env, sh: sh, forbidden: forbidden}, nil
}

// ResolveGateTool is resolveGateTool for a caller that runs a gate-side
// program itself (the lock policy's inputs evaluation).
func ResolveGateTool(tool string, forbidden []string) (string, error) {
	return resolveGateTool(tool, forbidden)
}

// resolveGateTool takes the tool's link-time pin when the build has one
// (spinclass#454), else finds it on the frontend's PATH; resolves its
// symlinks, and refuses one under a directory the worker writes. The scope
// prefix's program goes through it too (runHookInDirEnv).
func resolveGateTool(tool string, forbidden []string) (string, error) {
	p := embeds.GateTool(tool)
	if p == "" {
		var err error
		if p, err = exec.LookPath(tool); err != nil {
			return "", fmt.Errorf("guarded gate: %s is not on the frontend's PATH, so the gate cannot run: %w", tool, err)
		}
	}
	if resolved, rErr := filepath.EvalSymlinks(p); rErr == nil {
		p = resolved
	}
	if d := underAny(p, forbidden); d != "" {
		return "", fmt.Errorf("guarded gate: %s resolves to %s, under %s, which the worker writes; the gate cannot trust it", tool, p, d)
	}
	return p, nil
}

// underAny returns the first of dirs that contains p (symlinks in both
// resolved where they resolve), or "".
func underAny(p string, dirs []string) string {
	if p == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	p = filepath.Clean(p)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		for _, spelling := range []string{d, resolvedOr(d)} {
			spelling = filepath.Clean(spelling)
			if p == spelling || strings.HasPrefix(p, spelling+string(filepath.Separator)) {
				return d
			}
		}
	}
	return ""
}

func resolvedOr(d string) string {
	if r, err := filepath.EvalSymlinks(d); err == nil {
		return r
	}
	return d
}

// ConfinedPreMergeInDir runs cmd, the trusted [hooks].confined-pre-merge, as a
// guarded merge's gate in runDir (the detached build worktree at the landing
// sha): no direnv, the ConfinedGateEnv environment (forbidden as there), the
// same inactivity watchdog, cancellation and systemd scope as PreMergeInDir.
// sf supplies the watchdog and scope settings only; its pre-merge command is
// never read.
func ConfinedPreMergeInDir(ctx context.Context, sf sweatfile.Sweatfile, cmd, runDir string, forbidden []string, w io.Writer) error {
	g, err := newConfinedGate(runDir, forbidden)
	if err != nil {
		return err
	}
	command := cmd
	return withInactivityWatchdog(ctx, sf, w, func(ctx context.Context, w io.Writer) error {
		return runHookInDirEnv(ctx, &command, runDir, runDir, nil, 0, clown.ScopeIDFromContext(ctx), sf.RequireHookScope(), 0, w, &hermeticGate{env: g.env, sh: g.sh, forbidden: g.forbidden})
	})
}
