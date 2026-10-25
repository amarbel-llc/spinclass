// Package apply materializes a merged sweatfile into a worktree: the Claude
// Code settings, the .spinclass/env dotenv file, the direnv .envrc (and its
// allow record), and the per-worktree pre-commit repair hook (FDR 0019).
//
// Like internal/hookrun, it is split out of internal/0/sweatfile so the config
// SCHEMA package imports nothing else in the module: applying setup needs
// direnv and git, and under godyn's per-package compile a change here would
// otherwise invalidate every package that merely reads config
// (spinclass#309). The next step (spinclass#308) is to pull worktree.Create's
// setup orchestration down to this layer too.
package apply

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/log"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/safeio"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/direnv"
)

// Setup applies sf (merged with the built-in defaults) to worktreePath: Claude
// settings, the dotenv file, direnv, and the pre-commit hook.
func Setup(sf sweatfile.Sweatfile, worktreePath string) error {
	merged := sf.MergeWith(sweatfile.GetDefault())

	if err := ClaudeSettings(worktreePath, merged); err != nil {
		return fmt.Errorf("applying claude settings: %w", err)
	}

	if err := SpinclassEnv(sf, worktreePath); err != nil {
		return fmt.Errorf("writing .spinclass/env: %w", err)
	}

	// The dotenv file lived at the worktree top level before #121 moved
	// it inside .spinclass/; remove the stale copy best-effort so it
	// can't linger (and its old `dotenv .spinclass.env` .envrc directive
	// is rewritten by prepareDirenv below).
	_ = os.Remove(filepath.Join(worktreePath, ".spinclass.env"))

	if err := prepareDirenv(sf, worktreePath); err != nil {
		return err
	}

	// Install the per-session pre-commit repair hook (best-effort): a failure
	// here must never block session creation, so log and continue. No-op when
	// [hooks].pre-commit is inactive. See
	// docs/plans/2026-06-16-per-commit-repair-hook-design.md.
	if err := installPreCommitHook(merged, worktreePath); err != nil {
		log.Warn("pre-commit hook install skipped", "err", err)
	}

	return nil
}

func resolveSpinclassBinDir(worktreePath string) (string, error) {
	dir, err := git.CommonGitDir(worktreePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "spinclass", "bin"), nil
}

// envrcDirectives are the lines spinclass writes into a worktree's .envrc.
func envrcDirectives(sf sweatfile.Sweatfile, worktreePath string) ([]string, error) {
	var directives []string
	if sf.Direnv != nil && sf.Direnv.Envrc != nil {
		directives = append(directives, sf.Direnv.Envrc...)
	} else {
		directives = []string{"source_up"}
		if _, err := os.Stat(filepath.Join(worktreePath, "flake.nix")); err == nil {
			directives = append(directives, "use flake")
		}
	}
	if sf.Direnv != nil && len(sf.Direnv.Dotenv) > 0 {
		directives = append(directives, "dotenv .spinclass/env")
	}
	dirSpinclassBin, err := resolveSpinclassBinDir(worktreePath)
	if err != nil {
		return nil, err
	}
	dirSpinclassBinAbs, err := filepath.Abs(dirSpinclassBin)
	if err != nil {
		return nil, err
	}
	return append(directives, fmt.Sprintf("PATH_add \"%s\"", dirSpinclassBinAbs)), nil
}

func writeEnvrc(sf sweatfile.Sweatfile, worktreePath string) error {
	directives, err := envrcDirectives(sf, worktreePath)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(
		filepath.Join(worktreePath, ".envrc"),
		os.O_TRUNC|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	bufferedWriter := bufio.NewWriter(file)
	for _, directive := range directives {
		if _, err := fmt.Fprintln(bufferedWriter, directive); err != nil {
			return err
		}
	}
	return bufferedWriter.Flush()
}

// envrcTracked reports whether the repo tracks the worktree's .envrc.
func envrcTracked(worktreePath string) bool {
	_, err := git.Run(worktreePath, "ls-files", "--error-unmatch", "--", ".envrc")
	return err == nil
}

// keepTrackedEnvrc handles a .envrc the repo tracks. Spinclass never rewrites
// it: a spawn must not dirty a tracked file (round P, where the rewrite made a
// no-force close refuse). It is allowed only when the checked-out bytes equal
// the committed blob at HEAD (the freshened default branch at creation); a
// modified one is never allowed. This extends the trust the pre-merge hook
// already gives the repo's own flake and justfile (FDR 0035).
func keepTrackedEnvrc(sf sweatfile.Sweatfile, worktreePath string) error {
	if skipped, err := envrcDirectives(sf, worktreePath); err == nil {
		log.Warn("tracked .envrc left as checked out; spinclass's own directives were not added (the repo's .envrc can `dotenv_if_exists .spinclass/env`)",
			"skipped", strings.Join(skipped, "; "))
	}
	return allowTrackedEnvrcIfUnmodified(worktreePath)
}

// ReallowDirenv re-records `direnv allow` after a create hook may have changed
// .envrc (spinclass#213), under the same rule as setup: a tracked .envrc is
// allowed only while it is identical to HEAD.
func ReallowDirenv(worktreePath string) error {
	if envrcTracked(worktreePath) {
		return allowTrackedEnvrcIfUnmodified(worktreePath)
	}
	return AllowDirenv(worktreePath)
}

func allowTrackedEnvrcIfUnmodified(worktreePath string) error {
	head, herr := git.Run(worktreePath, "rev-parse", "--verify", "-q", "HEAD:.envrc")
	work, werr := git.Run(worktreePath, "hash-object", "--no-filters", "--", ".envrc")
	switch {
	case herr != nil || werr != nil:
		log.Warn("tracked .envrc: not allowed (its blob could not be compared with HEAD)", "decision", "skipped")
	case strings.TrimSpace(head) != strings.TrimSpace(work):
		log.Warn("tracked .envrc: not allowed (it differs from HEAD)", "decision", "modified", "head", strings.TrimSpace(head), "worktree", strings.TrimSpace(work))
	default:
		log.Info("tracked .envrc: allowed (identical to HEAD)", "decision", "allowed", "blob", strings.TrimSpace(work))
		return AllowDirenv(worktreePath)
	}
	return nil
}

// SpinclassEnv renders the merged [direnv.dotenv] map to
// <worktreePath>/.spinclass/env (the file the generated `dotenv .spinclass/env`
// .envrc line sources), expanding $WORKTREE to worktreePath and other $VARs
// from the process env, with keys sorted for a stable file. A no-op when no
// dotenv entries are declared. It never touches .envrc, so implicit
// (main-checkout) sessions can call it to receive dotenv values via a committed
// `dotenv_if_exists .spinclass/env` without their repo-owned .envrc being
// rewritten (#274); managed worktrees reach it through Setup.
func SpinclassEnv(sf sweatfile.Sweatfile, worktreePath string) error {
	if sf.Direnv == nil || len(sf.Direnv.Dotenv) == 0 {
		return nil
	}

	keys := make([]string, 0, len(sf.Direnv.Dotenv))
	for k := range sf.Direnv.Dotenv {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// A rebuild re-renders this into a worktree whose worker can write
	// .spinclass: never follow a link planted there (FDR 0035).
	if err := safeio.RealDir(filepath.Join(worktreePath, ".spinclass")); err != nil {
		return err
	}

	file, err := safeio.OpenFile(
		filepath.Join(worktreePath, ".spinclass", "env"),
		os.O_TRUNC|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	expand := func(key string) string {
		if key == "WORKTREE" {
			return worktreePath
		}
		return os.Getenv(key)
	}

	for _, k := range keys {
		expanded := os.Expand(sf.Direnv.Dotenv[k], expand)
		if _, err := fmt.Fprintf(file, "%s=%s\n", k, expanded); err != nil {
			return err
		}
	}

	return nil
}

func prepareDirenv(sf sweatfile.Sweatfile, worktreePath string) error {
	if _, ok := direnv.Resolve(); !ok {
		return nil
	}
	if envrcTracked(worktreePath) {
		return keepTrackedEnvrc(sf, worktreePath)
	}

	if err := writeEnvrc(sf, worktreePath); err != nil {
		return err
	}

	return AllowDirenv(worktreePath)
}

// AllowDirenv records a bare `direnv allow` for worktreePath's .envrc so a
// subsequently-loaded devshell is authorized — including the create hook's own
// `direnv exec` (hookrun), which refuses to load a blocked .envrc. This is
// deliberately the plain allow subcommand run against the worktree dir, NOT
// wrapped in `direnv exec`.
//
// No-op when direnv is unavailable or the worktree has no .envrc. Idempotent:
// safe to call again after a create hook may have mutated .envrc, which is how
// worktree.Create re-authorizes the final .envrc post-hook (fix #213).
func AllowDirenv(worktreePath string) error {
	direnvPath, ok := direnv.Resolve()
	if !ok {
		return nil
	}
	if !direnv.HasEnvrc(worktreePath) {
		return nil
	}

	cmd := exec.Command(direnvPath, "allow")
	cmd.Dir = worktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

// ClaudeSettings writes <worktreePath>/.claude/settings.local.json from sf's
// claude-allow rules and MCP allow-list, plus the .spinclass/ snapshot that
// `perms review` diffs against.
func ClaudeSettings(worktreePath string, sf sweatfile.Sweatfile) error {
	settingsPath := filepath.Join(
		worktreePath,
		".claude",
		"settings.local.json",
	)

	doc := make(map[string]any)

	permsMap, _ := doc["permissions"].(map[string]any)

	if permsMap == nil {
		permsMap = make(map[string]any)
	}

	var allRules []string
	if sf.Claude != nil {
		allRules = append(allRules, sf.Claude.Allow...)
	}

	// Edit(path) covers every file-editing tool (Read/Edit/Write/MultiEdit/
	// NotebookEdit); a path-scoped Write(...) rule is redundant and newer
	// Claude Code rejects it at startup with a validation warning.
	allRules = append(
		allRules,
		fmt.Sprintf("Read(%s/*)", worktreePath),
		fmt.Sprintf("Edit(%s/*)", worktreePath),
	)

	// Agrees with the spawn splice (spawn.AutoModeArgs) for a session started
	// without a --permission-mode flag. A confined spawn passes
	// --permission-mode dontAsk, which is meant to override this default.
	permsMap["defaultMode"] = "auto"
	if sf.SessionAutoModeDisabled() {
		permsMap["defaultMode"] = "acceptEdits"
	}
	permsMap["allow"] = allRules

	doc["permissions"] = permsMap

	// Auto-approve any user-declared MCP servers from the sweatfile's
	// effective allow-list (sweatfile [[mcps]] entries plus allowed-mcps).
	// The spinclass MCP server itself is loaded via the clown plugin and
	// does not need a session-local entry here.
	var enabledMCPs []string
	seen := map[string]bool{}
	for _, name := range sf.EffectiveAllowedMCPs() {
		if !seen[name] {
			seen[name] = true
			enabledMCPs = append(enabledMCPs, name)
		}
	}
	if len(enabledMCPs) > 0 {
		doc["enabledMcpjsonServers"] = enabledMCPs
	}

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(settingsPath, append(data, '\n'), 0o644); err != nil {
		return err
	}

	// Create .spinclass/ directory for spinclass-owned data (tool-use log,
	// settings snapshot) separate from Claude Code's .claude/ directory.
	spinclassDir := filepath.Join(worktreePath, ".spinclass")
	if err := safeio.RealDir(spinclassDir); err != nil {
		return err
	}

	// Write a snapshot so that `perms review` can diff against the baseline
	// and only surface rules added during the session.
	snapshotPath := filepath.Join(spinclassDir, ".settings-snapshot.json")
	return safeio.WriteFile(snapshotPath, append(data, '\n'), 0o644)
}
