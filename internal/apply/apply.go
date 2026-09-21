// Package apply materializes a merged sweatfile into a worktree: the Claude
// Code settings, the .spinclass/env dotenv file, the direnv .envrc (and its
// allow record), and the per-worktree pre-commit repair hook (FDR 0019).
//
// Like internal/hookrun, it is split out of internal/sweatfile so the config
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

	"github.com/charmbracelet/log"

	"code.linenisgreat.com/spinclass/internal/direnv"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
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

func writeEnvrc(sf sweatfile.Sweatfile, worktreePath string) error {
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

	var directives []string
	if sf.Direnv != nil && sf.Direnv.Envrc != nil {
		directives = sf.Direnv.Envrc
	} else {
		directives = []string{"source_up"}
		if _, err := os.Stat(filepath.Join(worktreePath, "flake.nix")); err == nil {
			directives = append(directives, "use flake")
		}
	}

	for _, directive := range directives {
		if _, err := fmt.Fprintln(bufferedWriter, directive); err != nil {
			return err
		}
	}

	if sf.Direnv != nil && len(sf.Direnv.Dotenv) > 0 {
		if _, err := fmt.Fprintln(bufferedWriter, "dotenv .spinclass/env"); err != nil {
			return err
		}
	}

	dirSpinclassBin, err := resolveSpinclassBinDir(worktreePath)
	if err != nil {
		return err
	}
	dirSpinclassBinAbs, err := filepath.Abs(dirSpinclassBin)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(
		bufferedWriter,
		"PATH_add \"%s\"\n",
		dirSpinclassBinAbs,
	); err != nil {
		return err
	}

	return bufferedWriter.Flush()
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

	if err := os.MkdirAll(filepath.Join(worktreePath, ".spinclass"), 0o755); err != nil {
		return err
	}

	file, err := os.OpenFile(
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

	permsMap["defaultMode"] = "acceptEdits"
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
	if err := os.MkdirAll(spinclassDir, 0o755); err != nil {
		return err
	}

	// Write a snapshot so that `perms review` can diff against the baseline
	// and only surface rules added during the session.
	snapshotPath := filepath.Join(spinclassDir, ".settings-snapshot.json")
	return os.WriteFile(snapshotPath, append(data, '\n'), 0o644)
}
