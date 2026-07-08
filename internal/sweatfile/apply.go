package sweatfile

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"

	"github.com/amarbel-llc/spinclass/internal/direnv"
)

func (sweatfile Sweatfile) Apply(worktreePath string) error {
	defaults := GetDefault()
	merged := sweatfile.MergeWith(defaults)

	if err := ApplyClaudeSettings(worktreePath, merged); err != nil {
		return fmt.Errorf("applying claude settings: %w", err)
	}

	if err := sweatfile.writeSpinclassEnv(worktreePath); err != nil {
		return fmt.Errorf("writing .spinclass/env: %w", err)
	}

	// The dotenv file lived at the worktree top level before #121 moved
	// it inside .spinclass/; remove the stale copy best-effort so it
	// can't linger (and its old `dotenv .spinclass.env` .envrc directive
	// is rewritten by prepareDirenv below).
	_ = os.Remove(filepath.Join(worktreePath, ".spinclass.env"))

	if err := sweatfile.prepareDirenv(worktreePath); err != nil {
		return err
	}

	// Install the per-session pre-commit repair hook (best-effort): a failure
	// here must never block session creation, so log and continue. No-op when
	// [hooks].pre-commit is inactive. See
	// docs/plans/2026-06-16-per-commit-repair-hook-design.md.
	if err := merged.installPreCommitHook(worktreePath); err != nil {
		log.Warn("pre-commit hook install skipped", "err", err)
	}

	return nil
}

func resolveSpinclassBinDir(worktreePath string) (string, error) {
	dir, err := gitCommonDir(worktreePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "spinclass", "bin"), nil
}

func (sf Sweatfile) writeEnvrc(worktreePath string) error {
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
		if _, ok := fileExists(filepath.Join(worktreePath, "flake.nix")); ok {
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

func (sf Sweatfile) writeSpinclassEnv(worktreePath string) error {
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

func (sf Sweatfile) prepareDirenv(worktreePath string) error {
	if _, ok := direnv.Resolve(); !ok {
		return nil
	}

	if err := sf.writeEnvrc(worktreePath); err != nil {
		return err
	}

	return sf.AllowDirenv(worktreePath)
}

// AllowDirenv records a bare `direnv allow` for worktreePath's .envrc so a
// subsequently-loaded devshell is authorized — including the create hook's own
// `direnv exec` (runHookInDir), which refuses to load a blocked .envrc. This is
// deliberately the plain allow subcommand run against the worktree dir, NOT
// wrapped in `direnv exec`.
//
// No-op when direnv is unavailable or the worktree has no .envrc. Idempotent:
// safe to call again after a create hook may have mutated .envrc, which is how
// worktree.Create re-authorizes the final .envrc post-hook (fix #213).
func (sf Sweatfile) AllowDirenv(worktreePath string) error {
	direnvPath, ok := direnv.Resolve()
	if !ok {
		return nil
	}
	if !worktreeHasEnvrc(worktreePath) {
		return nil
	}

	cmd := exec.Command(direnvPath, "allow")
	cmd.Dir = worktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

// worktreeHasEnvrc reports whether the worktree has a regular .envrc file,
// the precondition for devshell-scoping a hook via `direnv exec`. spinclass
// writes one (writeEnvrc) whenever direnv resolves; gating on its presence
// keeps the bare `sh -c` path for non-direnv repos so their hook behavior is
// unchanged.
func worktreeHasEnvrc(worktreePath string) bool {
	info, ok := fileExists(filepath.Join(worktreePath, ".envrc"))
	return ok && info.Mode().IsRegular()
}

func (sf Sweatfile) RunCreateHook(worktreePath string, w io.Writer) error {
	cmd := sf.CreateHookCommand()
	return runHook(cmd, worktreePath, w)
}

func (sf Sweatfile) RunPreMergeHook(worktreePath string, w io.Writer) error {
	return sf.RunPreMergeHookContext(context.Background(), worktreePath, w)
}

// RunRepairHookContext runs the [hooks].repair command (FDR 0018) in
// worktreePath, streaming combined stdout+stderr to w, and returns the
// command's exit status as its error. Callers gate on RepairActive() first; if
// repair is inactive this is a no-op returning nil. Unlike the pre-merge hook
// there is no inactivity watchdog — repair is a fast formatter/amend pass, not
// a minutes-long build/test hook.
func (sf Sweatfile) RunRepairHookContext(ctx context.Context, worktreePath string, w io.Writer) error {
	if !sf.RepairActive() {
		return nil
	}
	return runHookContext(ctx, sf.RepairHookCommand(), worktreePath, w)
}

// RunPreMergeHookContext runs the pre-merge hook bound to ctx, so a caller
// (the async job runner) can cancel/kill the hook subprocess. The synchronous
// path uses RunPreMergeHook, which passes a background context.
//
// When [hooks].inactivity-timeout is set, an activity watchdog wraps the hook:
// every output line bumps a last-activity timestamp, and a goroutine cancels
// the hook (killing the subprocess via exec.CommandContext) once it has been
// silent longer than the timeout. A genuinely hung hook is thus killed instead
// of running until the outer MCP/clown deadline. The watchdog ctx is a child of
// the caller's ctx, so an inactivity kill is distinguishable from a user cancel
// (e.g. session-job-cancel): only inactivity yields the dedicated error below.
func (sf Sweatfile) RunPreMergeHookContext(ctx context.Context, worktreePath string, w io.Writer) error {
	return sf.RunPreMergeHookInDir(ctx, worktreePath, worktreePath, w)
}

// RunPreMergeHookInDir runs the pre-merge hook with the devshell loaded from
// envDir (the session worktree, which has an allowed .envrc) while the hook's
// working directory is runDir (the detached build worktree pinned to the
// committed sha). The single-dir RunPreMergeHookContext is the envDir==runDir
// case (legacy in-place mode). See runHookInDir and FDR 0013.
func (sf Sweatfile) RunPreMergeHookInDir(ctx context.Context, envDir, runDir string, w io.Writer) error {
	cmd := sf.PreMergeHookCommand()
	timeout := sf.InactivityTimeoutValue()
	if timeout <= 0 {
		return runHookInDir(ctx, cmd, envDir, runDir, w)
	}

	aw := &activityWriter{w: w, last: time.Now()}
	hookCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var killedForInactivity atomic.Bool
	done := make(chan struct{})
	go func() {
		interval := timeout / 4
		if interval < time.Second {
			interval = time.Second
		}
		if interval > 15*time.Second {
			interval = 15 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-hookCtx.Done():
				return
			case <-ticker.C:
				if time.Since(aw.lastActivity()) > timeout {
					killedForInactivity.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	err := runHookInDir(hookCtx, cmd, envDir, runDir, aw)
	close(done)
	// Only surface the inactivity error when the hook actually failed because
	// of the kill; a hook that finished cleanly just before a late tick wins.
	if err != nil && killedForInactivity.Load() {
		return fmt.Errorf("pre-merge hook killed: no output for %s (inactivity-timeout)", timeout)
	}
	return err
}

// activityWriter wraps an io.Writer and records the time of the most recent
// Write. The inactivity watchdog in RunPreMergeHookContext reads lastActivity
// to decide whether the pre-merge hook has gone silent past its budget.
type activityWriter struct {
	w    io.Writer
	mu   sync.Mutex
	last time.Time
}

func (a *activityWriter) Write(p []byte) (int, error) {
	a.mu.Lock()
	a.last = time.Now()
	a.mu.Unlock()
	return a.w.Write(p)
}

func (a *activityWriter) lastActivity() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

func (sf Sweatfile) RunOnAttachHook(worktreePath string, w io.Writer) error {
	cmd := sf.OnAttachHookCommand()
	return runHook(cmd, worktreePath, w)
}

func (sf Sweatfile) RunOnDetachHook(worktreePath string, w io.Writer) error {
	cmd := sf.OnDetachHookCommand()
	return runHook(cmd, worktreePath, w)
}

func runHook(cmd *string, worktreePath string, w io.Writer) error {
	return runHookContext(context.Background(), cmd, worktreePath, w)
}

func runHookContext(ctx context.Context, cmd *string, worktreePath string, w io.Writer) error {
	return runHookInDir(ctx, cmd, worktreePath, worktreePath, w)
}

// runHookInDir runs a hook command with two distinct directory roles: envDir is
// the directory whose devshell (.envrc) is loaded via `direnv exec`, and runDir
// is the hook's working directory (cmd.Dir). For most hooks these coincide (the
// session worktree). They differ for the pre-merge hook, which runs in a
// detached build worktree (runDir) pinned to the committed sha but must load the
// devshell from the session worktree (envDir) — the build worktree is created
// from the tracked tree only, so it has neither the git-excluded .envrc nor a
// `direnv allow` record, whereas the session worktree has both (writeEnvrc +
// `direnv allow` at `sc start`). See spinclass#198 and FDR 0013.
func runHookInDir(ctx context.Context, cmd *string, envDir, runDir string, w io.Writer) error {
	if cmd == nil || *cmd == "" {
		return nil
	}

	script := stripEmptyLines(*cmd)
	if script == "" {
		return nil
	}

	if w == nil {
		w = io.Discard
	}

	// Run the hook inside the envDir devshell when one exists, so a hook command
	// provided by the repo's devShell (e.g. a flake-exposed conformist-repair)
	// resolves regardless of whether the spinclass process invoking the hook is
	// itself inside that devShell. Without this, a merge driven from a foreign
	// ambient env — e.g. ~/eng's update-nix-repos calling sc run, where the
	// merge phase runs in the orchestrator's PATH, not the sub-repo's — fails
	// with `command not found` (spinclass#198). Like sessionexec.CommandIn, which
	// devshell-scopes session-entry exec, but additionally gated on a real .envrc
	// in envDir so non-direnv repos keep the bare `sh -c`. `direnv exec <dir>`
	// loads the .envrc from <dir> independently of cmd.Dir, which lets the
	// pre-merge hook load the session worktree's allowed devshell while running
	// in the build worktree.
	// worktreeHasEnvrc (a single stat) is checked before direnv.Resolve (a PATH
	// scan when direnv is not build-pinned) so the common non-direnv repo skips
	// the lookup entirely.
	argv := []string{"sh", "-c", script}
	if worktreeHasEnvrc(envDir) {
		if direnvPath, ok := direnv.Resolve(); ok {
			argv = direnv.WrapExec(direnvPath, envDir, argv)
		}
	}

	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = runDir
	// Inherit os.Environ so SPINCLASS_* variables set by callers (or by
	// the running session) propagate into the hook. Always append WORKTREE
	// (the session worktree = envDir, the logical session location a hook
	// reasons about, not the transient build worktree) for backwards
	// compatibility with existing hook scripts.
	c.Env = append(os.Environ(), "WORKTREE="+envDir)
	c.Stdout = w
	c.Stderr = w

	return c.Run()
}

func stripEmptyLines(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func ApplyClaudeSettings(worktreePath string, sweatfile Sweatfile) error {
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
	if sweatfile.Claude != nil {
		allRules = append(allRules, sweatfile.Claude.Allow...)
	}

	allRules = append(
		allRules,
		fmt.Sprintf("Read(%s/*)", worktreePath),
		fmt.Sprintf("Edit(%s/*)", worktreePath),
		fmt.Sprintf("Write(%s/*)", worktreePath),
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
	for _, name := range sweatfile.EffectiveAllowedMCPs() {
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
