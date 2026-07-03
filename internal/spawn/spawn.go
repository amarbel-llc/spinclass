package spawn

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/shop"
	"github.com/amarbel-llc/spinclass/internal/spawnhandshake"
	"github.com/amarbel-llc/spinclass/internal/sweatfileio"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

// DefaultHelloDeadline bounds how long Launch waits for the worker's
// SessionStart hello. It is FDR 0006's documented tuning lever:
// comfortably past typical harness startup (10–30s), far inside MCP
// request timeouts. Raise it (or grow a knob) if cold nix-cache session
// starts exceed the window in practice.
const DefaultHelloDeadline = 60 * time.Second

// Result describes a successfully launched worker session.
type Result struct {
	SessionKey    string // <repo-dirname>/<branch> — the worker's chat target
	WorktreePath  string // absolute path to the worker's worktree
	MultiplexerID string // the worker's session key (= SPINCLASS_SESSION_ID, #146)
}

// Launch creates a detached, harness-booted worker session in repoPath and
// blocks until its SessionStart hello (FDR 0006). deadline 0 means
// DefaultHelloDeadline. The branch name and ResolvedPath are produced
// exactly as `sc start` does (worktree.ResolvePath), and the worktree is
// created via shop.Create — the existing non-attaching create path.
//
// On a hello timeout the worktree and its session state are intentionally
// left behind for inspection (`sc close` cleans them up).
func Launch(home, repoPath, driverKey, brief, desc string, deadline time.Duration) (Result, error) {
	var descArgs []string
	if desc != "" {
		descArgs = []string{desc}
	}
	rp, err := worktree.ResolvePath(repoPath, descArgs)
	if err != nil {
		return Result{}, err
	}

	// Validate the spawn templates BEFORE creating anything: bad config
	// (missing spawn-entry, no {entry} splice) must not litter a worktree.
	// Rendering pre-create skips the worktree's own (not yet checked out)
	// sweatfile layer, but that layer is the repo sweatfile checked out
	// fresh from the default branch — same content the repo layer already
	// contributed.
	argv, window, sessionEnv, err := renderSpawn(home, rp, brief)
	if err != nil {
		return Result{}, err
	}

	if _, err := shop.Create(io.Discard, rp, false, "", nil); err != nil {
		return Result{}, fmt.Errorf("creating worker worktree: %w", err)
	}

	return launchRendered(rp, driverKey, desc, deadline, argv, window, sessionEnv)
}

// LaunchExisting runs the post-worktree tail of Launch over an existing
// worktree: render the worker repo's spawn/spawn-entry templates (FIRST,
// so bad config fails before any state is written), write the worker's
// session state (spawned_by lineage), exec the spawn argv, and block on
// the chat hello. Task 7's detached fork reuses it over a
// worktree.CreateFrom-produced worktree.
func LaunchExisting(home string, rp worktree.ResolvedPath, driverKey, brief, desc string, deadline time.Duration) (Result, error) {
	argv, window, sessionEnv, err := renderSpawn(home, rp, brief)
	if err != nil {
		return Result{}, err
	}
	return launchRendered(rp, driverKey, desc, deadline, argv, window, sessionEnv)
}

// renderSpawn loads the WORKER repo's sweatfile hierarchy (its harness decides
// the spawn-entry, not the driver's) and renders the detached-harness argv. It
// also returns the hierarchy's [session-entry].env for the exec's environment.
// Safe to call before the worktree exists: LoadWorktreeHierarchy treats a
// missing leaf sweatfile as an empty layer.
func renderSpawn(home string, rp worktree.ResolvedPath, brief string) (argv, window []string, sessionEnv map[string]string, err error) {
	hierarchy, err := sweatfileio.LoadWorktreeHierarchy(home, rp.RepoPath, rp.AbsPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("loading worker sweatfile hierarchy: %w", err)
	}
	merged := hierarchy.Merged

	// FDR-0017 Piece 1: spinclass no longer wraps the worker in a multiplexer.
	// The detached-harness argv (clown's --clown-attach=spawn) is *expected* to
	// self-detach, but launchRendered no longer relies on that — startDetached
	// runs it in its own session and never waits on it, so a non-detaching entry
	// cannot wedge the launcher. {id} is not substituted here — the worker
	// derives its identity from SPINCLASS_SESSION_ID in the exec env (workerEnv).
	argv = SubstituteEntry(merged.SessionSpawnEntry(), brief, rp.AbsPath)
	window = SubstituteWindow(merged.SessionSpawnWindow(), rp.SessionKey, rp.AbsPath)
	return argv, window, merged.SessionEnv(), nil
}

// startDetached launches argv in its OWN session (setsid) with stdio wired to
// logPath — never the launcher's stdin/stdout/stderr — and does NOT wait for it
// to exit. It is the mechanism that makes every spawn "always detach": the
// child neither blocks the launcher (we never wait on its exit) nor tethers the
// launcher's stdio (so `sc spawn` / `sc fork` and the MCP tools return and their
// output pipes close as soon as the hello arrives), and it survives the
// launcher process exiting because it is a new session leader. A well-behaved
// entry (clown --clown-attach=spawn) still forks the worker and exits promptly;
// a misbehaving one that never returns can no longer wedge or tether the
// launcher. The child's own dup of the log fd keeps it writable after the
// launcher closes its copy; the reaper goroutine prevents a zombie in the
// long-lived serve process.
func startDetached(argv []string, dir string, env []string, logPath string) error {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("creating spawn log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening spawn log %s: %w", logPath, err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = nil // /dev/null: no controlling stdin
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("starting %q: %w", argv, err)
	}
	_ = logFile.Close() // the child holds its own dup; drop the launcher's copy
	go func() { _ = cmd.Wait() }()
	return nil
}

// launchSpawnWindow opens the configured terminal window onto the worker
// (#149), fire-and-forget: the window is a convenience side effect, so a
// start failure is a logged warning, never a spawn failure. Detached like the
// entry (startDetached) so the window process never tethers the launcher's
// stdio — otherwise `sc spawn` would not exit until the window did.
func launchSpawnWindow(argv []string, rp worktree.ResolvedPath, desc string, sessionEnv map[string]string) {
	if len(argv) == 0 {
		return
	}
	logPath := filepath.Join(rp.AbsPath, ".spinclass", "spawn-window.log")
	if err := startDetached(argv, rp.AbsPath, workerEnv(rp, desc, sessionEnv), logPath); err != nil {
		slog.Warn("spawn-window failed to start", "argv", argv, "err", err)
	}
}

// workerEnv builds the spawn exec's process environment. It MIRRORS
// executor.SessionExecutor.Attach's env construction (internal/executor/
// session.go) — keep the var list in sync. Deliberately duplicated rather
// than shared: the executor mutates the driver process env via os.Setenv
// (it execs in-place), while spawn must scope the worker identity to
// cmd.Env so the DRIVER's own SPINCLASS_* vars never leak to the worker
// through a multiplexer that propagates client env.
//
// os/exec keeps the LAST occurrence of a duplicated key, so order encodes
// precedence: inherited driver environ, then user [session-entry].env, then
// the spinclass-owned identity vars (appended last — authoritative).
func workerEnv(rp worktree.ResolvedPath, desc string, userEnv map[string]string) []string {
	// Strip the driver's inherited CLOWN_SESSION_ID/CLAUDE_SESSION_ID so the
	// worker's clown re-derives its channel from the worker's own
	// SPINCLASS_SESSION_ID (appended below). Otherwise the worker arms the
	// driver's channel and directed chat wakes to it are dropped (#169).
	env := session.StripInheritedSessionIDs(os.Environ())
	for k, v := range userEnv {
		env = append(env, k+"="+v)
	}

	// Split session key ("repo/branch") into individual env vars — same
	// derivation as SessionExecutor.Attach.
	repo, branch := rp.SessionKey, ""
	if i := strings.Index(rp.SessionKey, "/"); i >= 0 {
		repo, branch = rp.SessionKey[:i], rp.SessionKey[i+1:]
	}
	tmpDir := filepath.Join(rp.AbsPath, ".tmp")

	return append(
		env,
		"SPINCLASS_SESSION_ID="+rp.SessionKey,
		"SPINCLASS_REPO="+repo,
		"SPINCLASS_BRANCH="+branch,
		"SPINCLASS_WORKTREE="+rp.AbsPath,
		"SPINCLASS_DESCRIPTION="+desc,
		"TMPDIR="+tmpDir,
		"CLAUDE_CODE_TMPDIR="+tmpDir,
	)
}

// launchRendered is the shared tail of Launch and LaunchExisting: write the
// worker's session state, exec the already-rendered spawn argv with the
// worker's environment, and block on the chat hello.
func launchRendered(rp worktree.ResolvedPath, driverKey, desc string, deadline time.Duration, argv, window []string, sessionEnv map[string]string) (Result, error) {
	if deadline <= 0 {
		deadline = DefaultHelloDeadline
	}

	// PID 0 resolves "inactive" via ResolveState's PID-liveness until the
	// worker's SessionStart hook adopts the state (refreshing PID and
	// HelloSentAt — Task 5 of the spawn plan). That is correct pre-hello:
	// nothing is attached yet.
	st := session.State{
		PID:          0,
		SessionState: session.StateActive,
		RepoPath:     rp.RepoPath,
		WorktreePath: rp.AbsPath,
		Branch:       rp.Branch,
		SessionKey:   rp.SessionKey,
		Description:  desc,
		SpawnedBy:    driverKey,
		StartedAt:    time.Now().UTC(),
		Env: map[string]string{
			"SPINCLASS_SESSION_ID": rp.SessionKey,
		},
	}
	if err := session.Write(st); err != nil {
		return Result{}, fmt.Errorf("writing worker session state: %w", err)
	}

	// Captured BEFORE the exec so a hello racing worker startup still satisfies
	// the gate.
	startTime := time.Now()

	// spinclass GUARANTEES detachment rather than trusting the spawn-entry to
	// self-detach: startDetached launches the entry in its own session with
	// stdio redirected to a log and never waits for it to exit. Readiness is
	// proven SOLELY by the hello handshake below (bounded by deadline), so an
	// entry that never returns — a blocking direnv build, a foregrounded
	// clown/posh attach — can no longer wedge the launcher. A well-behaved
	// clown --clown-attach=spawn still forks the worker and exits promptly.
	logPath := filepath.Join(rp.AbsPath, ".spinclass", "spawn.log")
	if err := startDetached(argv, rp.AbsPath, workerEnv(rp, desc, sessionEnv), logPath); err != nil {
		return Result{}, fmt.Errorf("spawn template %q failed: %w", argv, err)
	}

	// Window BEFORE the hello wait — the user watches the harness boot
	// live (FDR 0006 #149 design: window-at-launch).
	launchSpawnWindow(window, rp, desc, sessionEnv)

	if err := spawnhandshake.WaitForHello(driverKey, rp.SessionKey, startTime, deadline); err != nil {
		return Result{}, err
	}

	return Result{
		SessionKey:    rp.SessionKey,
		WorktreePath:  rp.AbsPath,
		MultiplexerID: rp.SessionKey,
	}, nil
}
