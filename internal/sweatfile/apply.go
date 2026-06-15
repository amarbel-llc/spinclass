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

	"github.com/amarbel-llc/spinclass/internal/embeds"
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
	os.Remove(filepath.Join(worktreePath, ".spinclass.env"))

	if err := sweatfile.prepareDirenv(worktreePath); err != nil {
		return err
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
	defer file.Close()

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
	defer file.Close()

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
	direnvPath, ok := resolveDirenv()
	if !ok {
		return nil
	}

	if err := sf.writeEnvrc(worktreePath); err != nil {
		return err
	}

	cmd := exec.Command(direnvPath, "allow")
	cmd.Dir = worktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

// resolveDirenv returns the absolute path to the direnv binary, with
// the build-time-pinned value (from `lib.mkSpinclass`) taking
// precedence over PATH lookup. Returns ("", false) when direnv is
// unavailable in either location — callers treat that as "no direnv,
// skip envrc handling".
func resolveDirenv() (string, bool) {
	if pinned := embeds.DirenvBin(); pinned != "" {
		return pinned, true
	}
	path, err := exec.LookPath("direnv")
	if err != nil {
		return "", false
	}
	return path, true
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
	cmd := sf.PreMergeHookCommand()
	timeout := sf.InactivityTimeoutValue()
	if timeout <= 0 {
		return runHookContext(ctx, cmd, worktreePath, w)
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

	err := runHookContext(hookCtx, cmd, worktreePath, aw)
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

	c := exec.CommandContext(ctx, "sh", "-c", script)
	c.Dir = worktreePath
	// Inherit os.Environ so SPINCLASS_* variables set by callers (or by
	// the running session) propagate into the hook. Always append WORKTREE
	// for backwards compatibility with existing hook scripts.
	c.Env = append(os.Environ(), "WORKTREE="+worktreePath)
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
