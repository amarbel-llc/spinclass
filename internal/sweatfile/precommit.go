package sweatfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"code.linenisgreat.com/spinclass/internal/git"
)

// dispatcherName is the single dispatcher script in the worktree hooks dir;
// each shimmed hook name symlinks to it. git never runs it directly (its name
// is not a hook event), only the symlinks.
const dispatcherName = "_spinclass-dispatch"

// originalSentinelName records, inside .spinclass/, the hooks dir git would use
// absent our override. Persisted once: install re-runs idempotently on every
// resume, by which time core.hooksPath already points at our dir, so we must
// not re-derive the original from it (that would self-reference). See
// docs/plans/2026-06-17-precommit-hook-composition-design.md.
const originalSentinelName = "precommit-original-hooks"

// standardHookNames is the set of git hook event names we will shim when the
// original hooks dir provides one, so native hooks of every type keep firing
// under our per-worktree core.hooksPath (which otherwise shadows them all).
var standardHookNames = map[string]bool{
	"applypatch-msg": true, "pre-applypatch": true, "post-applypatch": true,
	"pre-commit": true, "pre-merge-commit": true, "prepare-commit-msg": true,
	"commit-msg": true, "post-commit": true, "pre-rebase": true,
	"post-checkout": true, "post-merge": true, "pre-push": true,
	"post-rewrite": true, "pre-auto-gc": true, "push-to-checkout": true,
	"sendemail-validate": true, "post-index-change": true, "fsmonitor-watchman": true,
	"pre-receive": true, "update": true, "post-receive": true, "post-update": true,
	"proc-receive": true, "reference-transaction": true,
}

// installPreCommitHook installs (or, when inactive, restores) the per-session
// pre-commit repair hook. When active it makes <worktree>/.spinclass/hooks a
// COMPOSING dispatcher: it runs the configured formatter on pre-commit and
// delegates to the repo's pre-existing native hooks (captured before our
// override), so native hooks of every type keep firing instead of being
// shadowed. Scoped to the worktree via extensions.worktreeConfig + a
// per-worktree core.hooksPath. When inactive it restores native-hooks-only.
//
// Errors are returned for the caller to log, but Apply treats them as non-fatal
// — a hook-install failure must never block session creation. See
// docs/plans/2026-06-17-precommit-hook-composition-design.md.
func (sf Sweatfile) installPreCommitHook(worktreePath string) error {
	hooksDir := filepath.Join(worktreePath, ".spinclass", "hooks")
	if abs, err := filepath.Abs(hooksDir); err == nil {
		hooksDir = abs
	}

	if !sf.PreCommitActive() {
		return restorePreCommitHook(worktreePath, hooksDir)
	}

	cmd := stripEmptyLines(*sf.PreCommitHookCommand())

	// Defensive: a core.worktree set in the COMMON config is the documented
	// extensions.worktreeConfig footgun. Refuse rather than risk the checkout.
	if commonConfigHasWorktreeOverride(worktreePath) {
		return fmt.Errorf("refusing to enable extensions.worktreeConfig: core.worktree is set in the common config")
	}

	original, err := resolveOriginalHooksDir(worktreePath, hooksDir)
	if err != nil {
		return err
	}

	// Rebuild our hooks dir deterministically so a native hook removed upstream
	// drops its stale shim. The sentinel lives in .spinclass/ (the parent), so
	// it survives this.
	if err := os.RemoveAll(hooksDir); err != nil {
		return err
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}

	dispatchPath := filepath.Join(hooksDir, dispatcherName)
	lockHash := flakeLockHash(worktreePath)
	if err := os.WriteFile(dispatchPath, []byte(dispatcherScript(original, hooksDir, cmd, lockHash)), 0o755); err != nil {
		return err
	}

	// Shim pre-commit (always, for our repair) plus every active native hook
	// the original dir provides (so they keep firing). Each is a symlink to the
	// dispatcher, which branches on its own basename.
	names := map[string]bool{"pre-commit": true}
	for _, n := range enumerateActiveHooks(original) {
		names[n] = true
	}
	for n := range names {
		if err := os.Symlink(dispatcherName, filepath.Join(hooksDir, n)); err != nil {
			return fmt.Errorf("linking %s hook: %w", n, err)
		}
	}

	if _, err := git.Run(worktreePath, "config", "extensions.worktreeConfig", "true"); err != nil {
		return fmt.Errorf("enabling extensions.worktreeConfig: %w", err)
	}
	if _, err := git.Run(worktreePath, "config", "--worktree", "core.hooksPath", hooksDir); err != nil {
		return fmt.Errorf("setting per-worktree core.hooksPath: %w", err)
	}

	return nil
}

// restorePreCommitHook uninstalls the hook so native hooks fire normally again:
// it unsets our per-worktree core.hooksPath (only if it is ours, so a
// user-managed value is left untouched) and removes our dispatcher dir and the
// sentinel. No-op when we were never installed. This makes
// [hooks].disable-pre-commit a true uninstall (and the rollback).
func restorePreCommitHook(worktreePath, hooksDir string) error {
	sentinel := filepath.Join(worktreePath, ".spinclass", originalSentinelName)
	_, sentErr := os.Stat(sentinel)

	cur, _ := git.Run(worktreePath, "config", "--worktree", "--get", "core.hooksPath")
	cur = strings.TrimSpace(cur)

	if sentErr != nil && cur != hooksDir {
		return nil // never installed
	}

	if cur == hooksDir {
		_, _ = git.Run(worktreePath, "config", "--worktree", "--unset", "core.hooksPath")
	}
	_ = os.RemoveAll(hooksDir)
	_ = os.Remove(sentinel)
	return nil
}

// resolveOriginalHooksDir returns the hooks dir git would use absent our
// override, reading the persisted sentinel first and capturing it on the first
// install. Capture: the effective core.hooksPath if set and not already ours
// (relative values resolved against the worktree top), else
// $GIT_COMMON_DIR/hooks. The result is guaranteed != hooksDir.
func resolveOriginalHooksDir(worktreePath, hooksDir string) (string, error) {
	sentinel := filepath.Join(worktreePath, ".spinclass", originalSentinelName)
	if data, err := os.ReadFile(sentinel); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			return s, nil
		}
	}

	original := ""
	if cur := strings.TrimSpace(mustConfig(worktreePath, "core.hooksPath")); cur != "" {
		abs := cur
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(worktreePath, abs)
		}
		if abs = filepath.Clean(abs); abs != hooksDir {
			original = abs
		}
	}
	if original == "" {
		common, err := gitCommonDir(worktreePath)
		if err != nil {
			return "", err
		}
		original = filepath.Join(common, "hooks")
	}

	if err := os.MkdirAll(filepath.Dir(sentinel), 0o755); err == nil {
		_ = os.WriteFile(sentinel, []byte(original+"\n"), 0o644)
	}
	return original, nil
}

// flakeLockHash returns git's content hash (`git hash-object`) of the
// worktree's flake.lock, or "" when there is no flake.lock or git cannot hash
// it. It is baked into the dispatcher as the session-start lock identity for the
// self-healing staleness check (spinclass#267). git-only by design: the
// dispatcher IS a git hook, so `git` is always available — no dependency on
// sha256sum/nix in the commit environment.
func flakeLockHash(worktreePath string) string {
	if _, err := os.Stat(filepath.Join(worktreePath, "flake.lock")); err != nil {
		return ""
	}
	out, err := git.Run(worktreePath, "hash-object", "flake.lock")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// mustConfig returns the effective git config value for key, or "" on error.
func mustConfig(worktreePath, key string) string {
	out, err := git.Run(worktreePath, "config", "--get", key)
	if err != nil {
		return ""
	}
	return out
}

// enumerateActiveHooks lists the standard git hook names present in dir as
// executable, non-.sample files (symlinks followed). pre-commit is excluded —
// the caller always shims it.
func enumerateActiveHooks(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if name == "pre-commit" || !standardHookNames[name] {
			continue
		}
		st, err := os.Stat(filepath.Join(dir, name)) // follows symlinks
		if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
			continue
		}
		out = append(out, name)
	}
	return out
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

// dispatcherData is the substitution set for dispatcherTemplate. Every field is
// single-quoted for POSIX sh by the template's `sq` func.
type dispatcherData struct {
	Orig     string // original hooks dir git would use absent our override
	Self     string // our per-worktree hooks dir (the exec-loop guard compares against it)
	Bin      string // the formatter's leading word, for the command-exists guard
	Cmd      string // the full formatter command line
	LockHash string // baked flake.lock hash for the #267 staleness check ("" disables it)
}

// dispatcherTemplate renders the composing dispatcher (see dispatcherScript for
// what the script does). Parsed once at init; a malformed template panics the
// package on load rather than emitting a broken hook at runtime.
var dispatcherTemplate = template.Must(
	template.New("dispatch").Funcs(template.FuncMap{"sq": shSingleQuote}).Parse(dispatcherTemplateText),
)

// dispatcherScript renders the composing dispatcher. Keyed on its own basename
// ($0), it runs the configured formatter on pre-commit (best-effort,
// non-blocking) and then delegates to the repo's original same-named hook,
// preserving its args, stdin, and exit code (so a blocking native lint still
// blocks the commit). A self-reference guard prevents an exec loop.
//
// A NONZERO formatter exit is surfaced loudly — its captured stderr is shown
// plus an explanatory banner — rather than silently swallowed. A stale or
// misconfigured formatter (e.g. a conformist that rejects its own canonical
// flags) would otherwise be indistinguishable from a working one, landing
// commits unformatted while looking fine (spinclass#183 review). The formatter
// is still non-blocking: the commit proceeds regardless.
//
// When flake.lock has drifted since the session's devShell froze (the baked
// LockHash), the formatter is re-evaluated via `nix develop --command` so a
// stale toolchain cannot restamp generated files (spinclass#267).
func dispatcherScript(originalDir, hooksDir, cmd, lockHash string) string {
	bin := cmd
	if i := strings.IndexAny(cmd, " \t"); i >= 0 {
		bin = cmd[:i]
	}
	var buf strings.Builder
	// Execute cannot fail here: the template is Must-parsed at init, `sq` is
	// infallible, every referenced field exists, and strings.Builder.Write never
	// errors — so the error is deliberately discarded.
	_ = dispatcherTemplate.Execute(&buf, dispatcherData{
		Orig:     originalDir,
		Self:     hooksDir,
		Bin:      bin,
		Cmd:      cmd,
		LockHash: lockHash,
	})
	return buf.String()
}

// dispatcherTemplateText is the composing dispatcher as a text/template. The
// script has no backticks, so it lives in a raw string literal: real newlines
// are line breaks and a literal \n inside a printf format survives verbatim. The
// {{.Field | sq}} actions inject POSIX-single-quoted values; the shell's own
// $0 / $@ / ${VAR} use single braces and never collide with template actions.
const dispatcherTemplateText = `#!/bin/sh
# Managed by spinclass — per-session pre-commit repair hook (composing dispatcher).
# Runs the configured formatter on pre-commit, then delegates to the repo's
# original same-named native hook (preserving its exit code). The formatter is
# best-effort/non-blocking, BUT a nonzero formatter exit is surfaced loudly (with
# its stderr) so a stale/misconfigured formatter is not a silent no-op. When the
# tree's flake.lock has drifted since this session's devShell was frozen, the
# formatter is re-evaluated in the CURRENT devShell (building it if needed) so a
# stale toolchain cannot restamp generated files (spinclass#267). Regenerated on
# every 'sc start' / 'sc resume'. See
# docs/plans/2026-06-17-precommit-hook-composition-design.md and
# docs/plans/2026-08-11-self-healing-precommit-dispatch-design.md.
hook=$(basename "$0")
orig={{.Orig | sq}}
self={{.Self | sq}}
if [ "$hook" = pre-commit ]; then
	bin={{.Bin | sq}}
	cmd={{.Cmd | sq}}
	lockhash={{.LockHash | sq}}
	# spinclass#267: re-eval the hook in the current devShell if flake.lock
	# drifted since this session's devShell was frozen (baked lockhash).
	via_nix=0
	if [ -n "$lockhash" ] && [ -f flake.lock ] && command -v nix >/dev/null 2>&1; then
		live=$(git hash-object flake.lock 2>/dev/null || printf '')
		if [ -n "$live" ] && [ "$live" != "$lockhash" ]; then
			printf '%s\n' "spinclass pre-commit: flake.lock changed since this session's devShell loaded; re-evaluating the hook in the current devShell (may build)." >&2
			via_nix=1
		fi
	fi
	if [ "$via_nix" = 1 ] || command -v "$bin" >/dev/null 2>&1; then
		err=$(mktemp 2>/dev/null || printf '%s' "${TMPDIR:-/tmp}/spinclass-precommit.$$")
		if [ "$via_nix" = 1 ]; then
			nix develop --command sh -c "$cmd" 2>"$err"
		else
			sh -c "$cmd" 2>"$err"
		fi
		rc=$?
		cat "$err" >&2
		rm -f "$err"
		if [ "$rc" -ne 0 ]; then
			printf '%s\n' \
				"spinclass pre-commit: formatter exited $rc — staged content NOT repaired (committing anyway)." \
				"  command: $cmd" \
				"  a flag/usage error here usually means the formatter is stale or misconfigured." >&2
		fi
	fi
fi
if [ "$orig" != "$self" ] && [ -x "$orig/$hook" ]; then
	exec "$orig/$hook" "$@"
fi
exit 0
`

// shSingleQuote wraps s in single quotes safe for POSIX sh, escaping any
// embedded single quotes via the '\” idiom.
func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
