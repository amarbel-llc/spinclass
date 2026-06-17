package sweatfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amarbel-llc/spinclass/internal/git"
)

// installPreCommitHook writes a generated pre-commit wrapper to
// <worktreePath>/.spinclass/hooks/pre-commit and points git at it scoped to
// THIS worktree only: extensions.worktreeConfig + a per-worktree
// core.hooksPath. Worktrees share $GIT_COMMON_DIR/hooks by default, so the
// per-worktree config is what keeps the hook from leaking into the main
// checkout or sibling worktrees (verified by `just explore-worktree-hooks`).
//
// It is a no-op when [hooks].pre-commit is inactive. Errors are returned for
// the caller to log, but Apply treats them as non-fatal — a hook-install
// failure must never block session creation. See
// docs/plans/2026-06-16-per-commit-repair-hook-design.md.
func (sf Sweatfile) installPreCommitHook(worktreePath string) error {
	if !sf.PreCommitActive() {
		return nil
	}

	cmd := stripEmptyLines(*sf.PreCommitHookCommand())

	// Defensive: a core.worktree set in the COMMON config is the documented
	// extensions.worktreeConfig footgun — once the extension is enabled it
	// would apply to every linked worktree. Refuse rather than risk the main
	// checkout; the hook is best-effort and skipping it is safe.
	if commonConfigHasWorktreeOverride(worktreePath) {
		return fmt.Errorf("refusing to enable extensions.worktreeConfig: core.worktree is set in the common config")
	}

	hooksDir := filepath.Join(worktreePath, ".spinclass", "hooks")
	if abs, err := filepath.Abs(hooksDir); err == nil {
		hooksDir = abs
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}

	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte(preCommitScript(cmd)), 0o755); err != nil {
		return err
	}

	if _, err := git.Run(worktreePath, "config", "extensions.worktreeConfig", "true"); err != nil {
		return fmt.Errorf("enabling extensions.worktreeConfig: %w", err)
	}
	if _, err := git.Run(worktreePath, "config", "--worktree", "core.hooksPath", hooksDir); err != nil {
		return fmt.Errorf("setting per-worktree core.hooksPath: %w", err)
	}

	return nil
}

// commonConfigHasWorktreeOverride reports whether core.worktree is set in the
// repository's COMMON config file (shared across all worktrees). When present,
// enabling extensions.worktreeConfig is unsafe (see installPreCommitHook).
func commonConfigHasWorktreeOverride(worktreePath string) bool {
	common, err := gitCommonDir(worktreePath)
	if err != nil {
		return false
	}
	cfgFile := filepath.Join(common, "config")
	out, err := git.Run(worktreePath, "config", "--file", cfgFile, "--get", "core.worktree")
	return err == nil && strings.TrimSpace(out) != ""
}

// preCommitScript renders the wrapper installed as the git pre-commit hook.
// It is deliberately best-effort and NON-BLOCKING:
//   - if the command's binary is not on PATH, exit 0 (a missing formatter never
//     blocks commits);
//   - run the configured command via `sh -c`; on exit 0 the commit proceeds
//     (conformant, or — with conformist fix A — reformatted-and-restaged);
//   - on any nonzero exit, warn to stderr and exit 0 anyway. A pre-A restage
//     (exit 3) has already restaged the formatted content; a refusal/error
//     proceeds unformatted. The hook never blocks a commit.
func preCommitScript(cmd string) string {
	bin := cmd
	if i := strings.IndexAny(cmd, " \t"); i >= 0 {
		bin = cmd[:i]
	}
	return "#!/bin/sh\n" +
		"# Managed by spinclass — per-session pre-commit repair hook.\n" +
		"# Best-effort and NON-BLOCKING: a missing formatter or any nonzero exit\n" +
		"# never blocks the commit. Regenerated on every `sc start`/`sc resume`.\n" +
		"# See docs/plans/2026-06-16-per-commit-repair-hook-design.md.\n" +
		"command -v " + shSingleQuote(bin) + " >/dev/null 2>&1 || exit 0\n" +
		"if ! sh -c " + shSingleQuote(cmd) + "; then\n" +
		"\techo 'spinclass pre-commit: formatter exited nonzero; committing anyway' >&2\n" +
		"fi\n" +
		"exit 0\n"
}

// shSingleQuote wraps s in single quotes safe for POSIX sh, escaping any
// embedded single quotes via the '\” idiom.
func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
