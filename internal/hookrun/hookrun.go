// Package hookrun executes the lifecycle hooks a sweatfile declares — create,
// on-attach/on-detach, pre-merge, repair, post-merge (the legacy string and
// the named [[post-merge]] targets) — plus the [auth] mint/revoke commands.
//
// It is deliberately separate from internal/sweatfile, which is the config
// SCHEMA (structs, hierarchy merge, the tommy codec, the accessors) and
// imports nothing else in the module. Running a hook needs clown (the #25
// systemd scope), direnv (devshell scoping) and process plumbing, and it is
// where the churn is; keeping that out of the schema package means a change to
// hook execution invalidates only the packages that execute hooks under
// godyn's per-package compile, not every package that merely reads config
// (spinclass#309).
//
// Every runner takes the merged sweatfile.Sweatfile as an argument and gates on
// the schema's own accessors (PreMergeHookCommand, RepairActive, …), so the
// contract of each hook is unchanged from when these were methods.
package hookrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/direnv"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// Create runs the [hooks].create command in worktreePath.
func Create(sf sweatfile.Sweatfile, worktreePath string, w io.Writer) error {
	return runHook(sf.CreateHookCommand(), worktreePath, w)
}

// OnAttach runs the [hooks].on-attach command in worktreePath.
func OnAttach(sf sweatfile.Sweatfile, worktreePath string, w io.Writer) error {
	return runHook(sf.OnAttachHookCommand(), worktreePath, w)
}

// OnDetach runs the [hooks].on-detach command in worktreePath, with
// sessionEnv (the SPINCLASS_* identity the hook reads, e.g.
// $SPINCLASS_SESSION_ID) appended. The session env is passed explicitly
// because it is no longer set on the spinclass process itself (#330).
func OnDetach(sf sweatfile.Sweatfile, worktreePath string, sessionEnv []string, w io.Writer) error {
	return runHookInDirEnv(context.Background(), sf.OnDetachHookCommand(), worktreePath, worktreePath, sessionEnv, 0, "", false, w)
}

// PreMerge runs the [hooks].pre-merge command in worktreePath under a
// background context. The cancellable form is PreMergeContext.
func PreMerge(sf sweatfile.Sweatfile, worktreePath string, w io.Writer) error {
	return PreMergeContext(context.Background(), sf, worktreePath, w)
}

// Repair runs the [hooks].repair command (FDR 0018) in worktreePath,
// streaming combined stdout+stderr to w, and returns the command's exit status
// as its error. Gated on RepairActive(): inactive is a no-op returning nil.
// Unlike the pre-merge hook there is no inactivity watchdog — repair is a fast
// formatter/amend pass, not a minutes-long build/test hook.
func Repair(ctx context.Context, sf sweatfile.Sweatfile, worktreePath string, w io.Writer) error {
	if !sf.RepairActive() {
		return nil
	}
	return runHookContext(ctx, sf.RepairHookCommand(), worktreePath, w)
}

// PostMerge runs the [hooks].post-merge command (FDR 0023) in dir, streaming
// combined stdout+stderr to w, with extraEnv (the SPINCLASS_MERGED_* facts)
// appended to the hook's environment. Gated on PostMergeActive(): inactive is
// a no-op returning nil.
//
// The hook is bounded by a WALL-CLOCK cap (PostMergeTimeoutValue: 10m by
// default, overridable via [hooks].post-merge-timeout, 0 to disable). It is
// capped by default — unlike the opt-in pre-merge inactivity watchdog —
// because post-merge runs under the per-repo landing lock, so a wedged hook
// holds the whole repo's merge queue (spinclass#246). Wall-clock rather than
// inactivity because a deploy can legitimately be silent for minutes.
//
// A cap kill is reported distinctly from a caller cancel (session-job-cancel),
// which cancels the parent ctx: the timeout message is only produced when the
// deadline fired while the parent was still live.
func PostMerge(ctx context.Context, sf sweatfile.Sweatfile, dir string, extraEnv []string, w io.Writer) error {
	return PostMergeWithCap(ctx, sf, dir, extraEnv, sf.PostMergeTimeoutValue(), w)
}

// PostMergeWithCap is PostMerge with the wall-clock cap supplied by the caller
// instead of read from the sweatfile: the merge phase resolves the EFFECTIVE
// cap (a per-merge override beats [hooks].post-merge-timeout beats the
// default) once and hands it here, so the legacy string path and the
// named-target path enforce — and advertise via SPINCLASS_POST_MERGE_TIMEOUT —
// the same number. timeout <= 0 disables the cap.
func PostMergeWithCap(ctx context.Context, sf sweatfile.Sweatfile, dir string, extraEnv []string, timeout time.Duration, w io.Writer) error {
	if !sf.PostMergeActive() {
		return nil
	}
	cmd := sf.PostMergeHookCommand()

	if timeout <= 0 {
		// Cap explicitly disabled: no deadline, and no WaitDelay either — the
		// operator asked for an unbounded hook, so draining a lingering child's
		// output is not ours to truncate.
		// scopeID "" — post-merge is deliberately unscoped so a control-group
		// kill never reaps the detached children FDR 0023 sanctions for slow deploys.
		return runHookInDirEnv(ctx, cmd, dir, dir, extraEnv, 0, "", false, w)
	}

	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err := runHookInDirEnv(hookCtx, cmd, dir, dir, extraEnv, postMergeWaitDelay, "", false, w)
	switch {
	// Only OUR deadline yields the cap message: if the caller's ctx is also
	// done, the kill is theirs (a cancelled job) and keeps its own error.
	case err != nil && errors.Is(hookCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		return fmt.Errorf(
			"post-merge hook killed: exceeded post-merge-timeout %s (the merge already landed; "+
				"set [hooks].post-merge-timeout, or pass post_merge_timeout on the merge call, to raise or 0 to disable the cap)",
			timeout,
		)
	// The hook itself finished, but left a child holding its output pipe, so
	// Wait had to be cut short. Worth naming precisely: the usual cause is a
	// backgrounded child that did not redirect stdout/stderr, which would
	// otherwise hold the merge lock for the child's full lifetime.
	case errors.Is(err, exec.ErrWaitDelay):
		return fmt.Errorf(
			"post-merge hook left a child holding its output pipe; stopped waiting after %s "+
				"(redirect backgrounded work off the hook's stdout/stderr, e.g. `… >>deploy.log 2>&1 &`)",
			postMergeWaitDelay,
		)
	}
	return err
}

// Target runs one named [[post-merge]] target (FDR 0026) in dir, streaming the
// combined stdout+stderr of both stages to w with extraEnv (the
// SPINCLASS_MERGED_* facts) appended. It runs Command; only if Command exits
// zero AND the target declares a non-empty Verify does it run Verify.
//
// Unlike PostMerge (the legacy single-string path, which derives its own cap),
// this applies NO timeout of its own: the named-target phase owns one shared
// wall-clock deadline across all targets (so N targets cannot hold the merge
// lock past post-merge-timeout), and passes it in via ctx. Each stage runs
// under that ctx with the post-merge WaitDelay/drain, so a backgrounded child
// that forgot to redirect its output cannot hold the lock for its full
// lifetime.
//
// Returns the verdict — PostMergeOK, PostMergeCommandFailed, or
// PostMergeVerifyFailed — and, for a failed stage, the underlying error (which
// the phase inspects against the shared deadline to distinguish a genuine
// failure from a cap kill).
func Target(ctx context.Context, t sweatfile.PostMergeTarget, dir string, extraEnv []string, w io.Writer) (sweatfile.PostMergeVerdict, error) {
	command := t.Command
	// scopeID "" — post-merge is deliberately unscoped so a control-group
	// kill never reaps the detached children FDR 0023 sanctions for slow deploys.
	if err := runHookInDirEnv(ctx, &command, dir, dir, extraEnv, postMergeWaitDelay, "", false, w); err != nil {
		return sweatfile.PostMergeCommandFailed, err
	}
	if t.HasVerify() {
		verify := *t.Verify
		if err := runHookInDirEnv(ctx, &verify, dir, dir, extraEnv, postMergeWaitDelay, "", false, w); err != nil {
			return sweatfile.PostMergeVerifyFailed, err
		}
	}
	return sweatfile.PostMergeOK, nil
}

// cancelGrace is how long a cancelled hook has to exit after SIGTERM before
// exec escalates: closing its I/O pipes and sending SIGKILL. It therefore
// doubles as the universal upper bound on how long a cancel can appear to
// hang — the failure #188 reports, where an orphaned `nix` held the pipe for
// 224s. Generous enough for a large build to notice a signal and unwind,
// short enough that no cancel is mistaken for a wedge.
const cancelGrace = 10 * time.Second

// postMergeWaitDelay bounds how long Wait keeps draining the hook's output
// pipe after the hook process itself is done or killed. It is the difference
// between post-merge-timeout being a real wall-clock bound and being advisory
// — see runHookInDirEnv. Generous enough to collect a normal hook's trailing
// output, short enough that a lingering child cannot hold the merge lock.
const postMergeWaitDelay = 5 * time.Second

// PreMergeContext runs the pre-merge hook bound to ctx, so a caller (the async
// job runner) can cancel/kill the hook subprocess. The synchronous path uses
// PreMerge, which passes a background context.
//
// When [hooks].inactivity-timeout is set, an activity watchdog wraps the hook:
// every output line bumps a last-activity timestamp, and a goroutine cancels
// the hook (killing the subprocess via exec.CommandContext) once it has been
// silent longer than the timeout. A genuinely hung hook is thus killed instead
// of running until the outer MCP/clown deadline. The watchdog ctx is a child of
// the caller's ctx, so an inactivity kill is distinguishable from a user cancel
// (e.g. session-job-cancel): only inactivity yields the dedicated error below.
func PreMergeContext(ctx context.Context, sf sweatfile.Sweatfile, worktreePath string, w io.Writer) error {
	return PreMergeInDir(ctx, sf, worktreePath, worktreePath, w)
}

// PreMergeInDir runs the pre-merge hook with the devshell loaded from envDir
// (the session worktree, which has an allowed .envrc) while the hook's working
// directory is runDir (the detached build worktree pinned to the committed
// sha). The single-dir PreMergeContext is the envDir==runDir case (legacy
// in-place mode). See runHookInDir and FDR 0013.
func PreMergeInDir(ctx context.Context, sf sweatfile.Sweatfile, envDir, runDir string, w io.Writer) error {
	cmd := sf.PreMergeHookCommand()
	timeout := sf.InactivityTimeoutValue()
	if timeout <= 0 {
		return runHookInDir(ctx, cmd, envDir, runDir, sf.RequireHookScope(), w)
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

	err := runHookInDir(hookCtx, cmd, envDir, runDir, sf.RequireHookScope(), aw)
	close(done)
	// Only surface the inactivity error when the hook actually failed because
	// of the kill; a hook that finished cleanly just before a late tick wins.
	if err != nil && killedForInactivity.Load() {
		return fmt.Errorf("pre-merge hook killed: no output for %s (inactivity-timeout)", timeout)
	}
	return err
}

// activityWriter wraps an io.Writer and records the time of the most recent
// Write. The inactivity watchdog in PreMergeInDir reads lastActivity to decide
// whether the pre-merge hook has gone silent past its budget.
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

func runHook(cmd *string, worktreePath string, w io.Writer) error {
	return runHookContext(context.Background(), cmd, worktreePath, w)
}

func runHookContext(ctx context.Context, cmd *string, worktreePath string, w io.Writer) error {
	// These hooks run under a ctx with no scope id, so requireScope is moot.
	return runHookInDir(ctx, cmd, worktreePath, worktreePath, false, w)
}

// runHookInDir runs a hook command with two distinct directory roles: envDir is
// the directory whose devshell (.envrc) is loaded via `direnv exec`, and runDir
// is the hook's working directory (cmd.Dir). For most hooks these coincide (the
// session worktree). They differ for the pre-merge hook, which runs in a
// detached build worktree (runDir) pinned to the committed sha but must load the
// devshell from the session worktree (envDir) — the build worktree is created
// from the tracked tree only, so it has neither the git-excluded .envrc nor a
// `direnv allow` record, whereas the session worktree has both (apply.Setup +
// `direnv allow` at `sc start`). See spinclass#198 and FDR 0013.
func runHookInDir(ctx context.Context, cmd *string, envDir, runDir string, requireScope bool, w io.Writer) error {
	// The pre-merge hook runs under a ctx that carries a scope id: the async job
	// id (clown.WithJobID, set in job.Start) or a synchronous gate's local scope
	// id (clown.WithLocalScope, #188). Reading it here scopes that hook (#25),
	// while repair/create/attach/detach — run under context.Background — get "" and
	// stay unscoped. Post-merge calls runHookInDirEnv directly with "" so FDR 0023
	// detached children are not caught in a control-group scope kill.
	return runHookInDirEnv(ctx, cmd, envDir, runDir, nil, 0, clown.ScopeIDFromContext(ctx), requireScope, w)
}

// runHookInDirEnv is runHookInDir with a few extras; every hook that needs none
// passes the zero values via runHookInDir.
//
// extraEnv is appended to the hook's environment (after os.Environ and
// WORKTREE, so it wins on duplicate keys) to publish the landed sha and push
// state the hook acts on. Used only by the post-merge hook.
//
// waitDelay, when non-zero, tightens the post-exit drain bound below the
// default cancelGrace; callers that do not care pass 0 and get cancelGrace.
// Used only by the post-merge hook.
//
// scopeID, when non-empty AND the scope tier is available, runs the hook
// inside its transient systemd scope (#25, RFC-0016): the argv is prefixed with
// clown.ScopeArgv's `systemd-run --user --scope …` so a wedged or detached hook
// subtree is reaped as a control group on cancel (via clown.ScopeStop below),
// above the SIGTERM/WaitDelay floor. Only the pre-merge path passes it non-empty
// (runHookInDir reads the async job id or a synchronous gate's local scope id
// from ctx); post-merge passes "" so its
// FDR-0023 detached children are not caught in the control-group kill.
//
// requireScope ([hooks].require-hook-scope) decides what a FAILED scope setup
// means when the tier is available: false falls back to the bare hook with a
// warning, true fails the hook without running it. See runHookInScope. It has
// no effect when scopeID is "" or the tier is unavailable.
//
// Cancellation semantics matter more than they look, and were measured
// (spinclass#188). exec.CommandContext's DEFAULT Cancel is Process.Kill() —
// SIGKILL, which cannot be trapped. The hook's argv collapses by exec
// (`direnv exec <dir> sh -c <script>`: direnv execs into sh, sh execs into the
// single command), so cmd.Process is the hook command itself, e.g. `just`.
// SIGKILLing it denies it any chance to tear down its own children, so a
// `nix` it spawned is orphaned still holding the inherited stdout/stderr —
// and Wait cannot return until every holder of that pipe closes it. Measured
// on a real gate: pipe still held 224s after the kill.
//
// So Cancel is overridden to SIGTERM, which `just` (and any well-behaved hook
// runner) propagates to its children: the same probe freed the pipe in under a
// second. WaitDelay is the escalation — if the hook has not exited by then,
// exec closes the pipes and sends SIGKILL, so an ill-behaved hook still cannot
// wedge a cancel forever.
//
// Deliberately NO Setpgid/process-group signalling: a group kill would also
// reap the DETACHED children FDR 0023 documents as the supported way to run a
// slow post-merge deploy without holding the merge lock. Residual: a hook
// whose top process swallows SIGTERM without propagating leaves its
// descendants orphaned, and only the pipes are reclaimed. When that escalation
// fires with no scope to reap the children, a warning line is written into the
// hook output so the operator knows descendants may still be running.
func runHookInDirEnv(ctx context.Context, cmd *string, envDir, runDir string, extraEnv []string, waitDelay time.Duration, scopeID string, requireScope bool, w io.Writer) error {
	if cmd == nil || *cmd == "" {
		return nil
	}

	script := sweatfile.NormalizeCommand(*cmd)
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
	// direnv.HasEnvrc (a single stat) is checked before direnv.Resolve (a PATH
	// scan when direnv is not build-pinned) so the common non-direnv repo skips
	// the lookup entirely.
	argv := []string{"sh", "-c", script}
	if direnv.HasEnvrc(envDir) {
		if direnvPath, ok := direnv.Resolve(); ok {
			argv = direnv.WrapExec(direnvPath, envDir, argv)
		}
	}

	// #25: when a scope id is present and the scope tier is available, wrap the
	// whole command (outermost) in its transient systemd scope, so a hook
	// subtree that ignores SIGTERM is still reaped as a control group by the
	// ScopeStop on cancel below. Prepended AFTER the direnv wrap so systemd-run is
	// argv[0]. Unavailable (no systemd user bus, or RINGMASTER_DISABLE_SCOPE) means
	// the hook runs bare and the #26 flock stays the liveness floor.
	//
	// A Cmd cannot be reused, and a failed scope setup re-runs the bare argv, so
	// the command is built by a closure.
	newHookCmd := func(argv []string, out io.Writer) *exec.Cmd {
		c := exec.CommandContext(ctx, argv[0], argv[1:]...)
		c.Dir = runDir
		// Inherit os.Environ so SPINCLASS_* variables set by callers (or by
		// the running session) propagate into the hook. Always append WORKTREE
		// (the session worktree = envDir, the logical session location a hook
		// reasons about, not the transient build worktree) for backwards
		// compatibility with existing hook scripts.
		c.Env = append(os.Environ(), "WORKTREE="+envDir)
		c.Env = append(c.Env, extraEnv...)
		c.Stdout = out
		c.Stderr = out

		// See the doc comment: SIGTERM so the hook can tear down its own process
		// tree, with WaitDelay as the SIGKILL escalation. Every hook gets an
		// escalation bound — a caller-supplied waitDelay only tightens it.
		c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
		c.WaitDelay = cancelGrace
		if waitDelay > 0 && waitDelay < cancelGrace {
			c.WaitDelay = waitDelay
		}
		return c
	}

	var (
		c      *exec.Cmd
		err    error
		scoped bool
	)
	if scopeID != "" {
		if prefix, ok := scopeArgv(scopeID); ok {
			c, scoped, err = runHookInScope(ctx, newHookCmd, prefix, argv, requireScope, w)
			if !scoped && err != nil {
				return err
			}
		}
	}
	// No scope tier, or its setup failed before the hook started (already
	// warned about): the bare hook. scoped is false, so the escalation warning
	// below applies to it.
	if !scoped {
		c = newHookCmd(argv, w)
		err = c.Run()
	}
	if !scoped && ctx.Err() != nil && escalated(c, err) {
		_, _ = fmt.Fprintf(w, "[spinclass] hook ignored SIGTERM for %s and was killed; "+
			"with no systemd scope its child processes may still be running (spinclass#188)\n",
			c.WaitDelay)
	}
	if scoped && ctx.Err() != nil {
		// The hook's ctx was cancelled (inactivity watchdog, #22 observer, or
		// session-job-cancel). SIGTERM + WaitDelay already fired at the front
		// systemd-run process; stop the scope to reap the whole cgroup as the
		// backstop for a subtree that ignored SIGTERM. Fresh bounded ctx (the
		// hook ctx is already cancelled); the job is ending, so a failed stop is
		// logged, not surfaced.
		stopCtx, stopCancel := context.WithTimeout(context.Background(), cancelGrace)
		defer stopCancel()
		if serr := clown.ScopeStop(stopCtx, scopeID); serr != nil {
			_, _ = fmt.Fprintf(w, "[clown] scope stop failed: %v\n", serr)
		}
	}
	return err
}

// scopeArgv is clown.ScopeArgv behind a seam, so tests can stand in a scope
// prefix that fails (or works) deterministically without a systemd user bus.
var scopeArgv = clown.ScopeArgv

// runHookInScope runs argv under the scope prefix and reports whether the hook
// ran there. ran=false with a nil error means the scope could not be set up and
// the caller should run the hook bare (a warning has been written to w);
// ran=false with an error means stop: the ctx was cancelled before the hook
// started, or [hooks].require-hook-scope forbids the unscoped fallback.
//
// The fallback is transitional (spinclass#188): require-hook-scope is opt-in
// now, is expected to become the default, and the fallback is then removed.
//
// It must never re-run a hook that started, and systemd-run exits with the
// hook's own status, so an exit code cannot tell "scope refused" from "hook
// failed". The scopeStartMarker does: no mark means the hook's argv was never
// exec'd, whatever systemd-run returned (including a spawn error or exit 0).
func runHookInScope(
	ctx context.Context,
	newHookCmd func(argv []string, out io.Writer) *exec.Cmd,
	prefix, argv []string,
	requireScope bool,
	w io.Writer,
) (c *exec.Cmd, ran bool, err error) {
	marker, err := newScopeStartMarker()
	if err == nil {
		defer marker.discard()
		complaint := &firstLineCapture{w: w}
		c = newHookCmd(append(append([]string(nil), prefix...), marker.wrap(argv)...), complaint)
		err = c.Run()
		if marker.hookStarted() {
			return c, true, err
		}
		// Everything written so far came from the scope setup, not the hook.
		if err == nil {
			err = errors.New("exited 0 without running the hook")
		}
		if line := complaint.firstLine(); line != "" {
			err = fmt.Errorf("%w: %s", err, line)
		}
	}

	switch {
	case ctx.Err() != nil:
		// A cancel that landed before the hook started leaves no mark either;
		// it is a cancel, not a setup failure.
		return nil, false, err
	case requireScope:
		return nil, false, fmt.Errorf(
			"pre-merge hook not run: its systemd scope could not be set up and [hooks].require-hook-scope is set: %w", err)
	}
	_, _ = fmt.Fprintf(w, "[spinclass] hook scope unavailable (%v); running unscoped\n", err)
	return nil, false, nil
}

// scopeStartMarker proves whether a scoped hook started. The wrapper it puts in
// front of the hook's argv runs INSIDE the scope and appends to the marker file
// strictly before exec'ing the hook, `&&`-chained: an unmarked file means the
// exec was never reached. It execs, so the process the cancel signals is still
// the hook itself (see runHookInDirEnv).
//
// The mark is read through the fd opened at creation, not by path: a hook that
// wipes the temp dir unlinks the name but cannot make a started hook look
// unstarted. For the same reason the wrapper refuses to run the hook when the
// file is already gone (it would otherwise mark a new inode nobody reads). The
// file lives in the system temp dir, outside the build worktree, one per run.
type scopeStartMarker struct{ file *os.File }

const scopeStartScript = `mark=$1; shift; [ -f "$mark" ] && echo started >>"$mark" && exec "$@"`

func newScopeStartMarker() (*scopeStartMarker, error) {
	file, err := os.CreateTemp("", "spinclass-hook-scope-*")
	if err != nil {
		return nil, fmt.Errorf("creating the scope start marker: %w", err)
	}
	return &scopeStartMarker{file: file}, nil
}

func (m *scopeStartMarker) wrap(argv []string) []string {
	return append([]string{"sh", "-c", scopeStartScript, "sh", m.file.Name()}, argv...)
}

// hookStarted errs toward true: an unreadable marker must not license a re-run.
func (m *scopeStartMarker) hookStarted() bool {
	info, err := m.file.Stat()
	return err != nil || info.Size() > 0
}

func (m *scopeStartMarker) discard() {
	_ = m.file.Close()
	_ = os.Remove(m.file.Name())
}

// firstLineCapture passes writes through to w and keeps the first line, so a
// failed scope setup can be reported with systemd-run's own complaint.
type firstLineCapture struct {
	w    io.Writer
	head []byte
	done bool
}

const maxCapturedLine = 512

func (f *firstLineCapture) Write(p []byte) (int, error) {
	if !f.done {
		chunk := p
		if i := bytes.IndexByte(chunk, '\n'); i >= 0 {
			chunk, f.done = chunk[:i], true
		}
		f.head = append(f.head, chunk[:min(len(chunk), maxCapturedLine-len(f.head))]...)
		f.done = f.done || len(f.head) == maxCapturedLine
	}
	return f.w.Write(p)
}

func (f *firstLineCapture) firstLine() string { return strings.TrimSpace(string(f.head)) }

// escalated reports whether a cancelled command needed the WaitDelay escalation:
// either the top process exited by itself but a descendant still held the pipe
// when WaitDelay expired (exec.ErrWaitDelay), or the top process was SIGKILLed
// (Wait then returns the ExitError, not ErrWaitDelay). Either way a descendant
// may have survived.
func escalated(c *exec.Cmd, err error) bool {
	if errors.Is(err, exec.ErrWaitDelay) {
		return true
	}
	if c.ProcessState == nil {
		return false
	}
	ws, ok := c.ProcessState.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

// CommandCapture runs a sweatfile-declared command (`sh -c`) in dir,
// devshell-scoped exactly like the lifecycle hooks (`direnv exec` when dir has
// an .envrc), with extraEnv appended to the process env, and returns its
// stdout. Unlike the hooks, the command's RESULT is its stdout — the
// [auth].mint-command's token (FDR 0028) — so stdout is captured rather than
// streamed; stderr is folded into the error on failure.
func CommandCapture(ctx context.Context, dir, cmd string, extraEnv []string) (string, error) {
	script := sweatfile.NormalizeCommand(cmd)
	if script == "" {
		return "", errors.New("empty command")
	}
	argv := []string{"sh", "-c", script}
	if direnv.HasEnvrc(dir) {
		if direnvPath, ok := direnv.Resolve(); ok {
			argv = direnv.WrapExec(direnvPath, dir, argv)
		}
	}
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = dir
	c.Env = append(append(os.Environ(), "WORKTREE="+dir), extraEnv...)
	var stdout, stderr strings.Builder
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return stdout.String(), nil
}

// maxAmbientCapture bounds each of CommandCaptureAmbient's captured streams.
const maxAmbientCapture = 64 << 10

// cappedBuffer keeps the first maxAmbientCapture bytes written and discards the
// rest without failing the writer (a failed Write would kill the child).
type cappedBuffer struct{ b []byte }

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := maxAmbientCapture - len(c.b); room > 0 {
		c.b = append(c.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return string(c.b) }

// CommandCaptureAmbient is CommandCapture without the devshell: `sh -c` in dir
// on the ambient PATH, no `direnv exec`, no WORKTREE injection. Used for
// commands whose answer decides where a credential goes ([auth].url-resolver):
// the devshell is head-controlled (a branch can change it), so running such a
// command inside it would let the branch choose the resolver's tools (FDR
// 0031's reasoning). stdout is returned, stderr is folded into errors, and
// WaitDelay bounds a grandchild that outlives the shell holding the pipes.
// The inherited env drops every DIRENV_* variable (so a caller running inside
// a devshell does not leak direnv's loaded-state markers; PATH itself is still
// inherited, that being the "ambient" decision), and stdout and stderr are each
// captured up to 64 KiB, silently truncated beyond that.
func CommandCaptureAmbient(ctx context.Context, dir, cmd string, extraEnv []string) (string, error) {
	script := sweatfile.NormalizeCommand(cmd)
	if script == "" {
		return "", errors.New("empty command")
	}
	c := exec.CommandContext(ctx, "sh", "-c", script)
	c.Dir = dir
	env := make([]string, 0, len(os.Environ())+len(extraEnv))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DIRENV_") {
			env = append(env, kv)
		}
	}
	c.Env = append(env, extraEnv...)
	c.WaitDelay = 2 * time.Second
	stdout, stderr := &cappedBuffer{}, &cappedBuffer{}
	c.Stdout = stdout
	c.Stderr = stderr
	if err := c.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return stdout.String(), nil
}
