package spawn

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/amarbel-llc/spinclass/internal/chat"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/shop"
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
	MultiplexerID string // the {id} the spawn template received (the branch)
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
	argv, sessionEnv, err := renderSpawn(home, rp, brief)
	if err != nil {
		return Result{}, err
	}

	if err := shop.Create(io.Discard, rp, false, "", nil); err != nil {
		return Result{}, fmt.Errorf("creating worker worktree: %w", err)
	}

	return launchRendered(rp, driverKey, desc, deadline, argv, sessionEnv)
}

// LaunchExisting runs the post-worktree tail of Launch over an existing
// worktree: render the worker repo's spawn/spawn-entry templates (FIRST,
// so bad config fails before any state is written), write the worker's
// session state (spawned_by lineage), exec the spawn argv, and block on
// the chat hello. Task 7's detached fork reuses it over a
// worktree.CreateFrom-produced worktree.
func LaunchExisting(home string, rp worktree.ResolvedPath, driverKey, brief, desc string, deadline time.Duration) (Result, error) {
	argv, sessionEnv, err := renderSpawn(home, rp, brief)
	if err != nil {
		return Result{}, err
	}
	return launchRendered(rp, driverKey, desc, deadline, argv, sessionEnv)
}

// renderSpawn loads the WORKER repo's sweatfile hierarchy (its multiplexer
// and harness decide the templates, not the driver's) and renders the
// spawn/spawn-entry argv. It also returns the hierarchy's [session-entry].env
// for the exec's environment. Safe to call before the worktree exists:
// LoadWorktreeHierarchy treats a missing leaf sweatfile as an empty layer.
func renderSpawn(home string, rp worktree.ResolvedPath, brief string) ([]string, map[string]string, error) {
	hierarchy, err := sweatfileio.LoadWorktreeHierarchy(home, rp.RepoPath, rp.AbsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("loading worker sweatfile hierarchy: %w", err)
	}
	merged := hierarchy.Merged

	entry := SubstituteEntry(merged.SessionSpawnEntry(), brief, rp.AbsPath)
	argv, err := SubstituteSpawn(merged.SessionSpawn(), rp.Branch, rp.AbsPath, entry)
	if err != nil {
		return nil, nil, err
	}
	return argv, merged.SessionEnv(), nil
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
	env := os.Environ()
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
func launchRendered(rp worktree.ResolvedPath, driverKey, desc string, deadline time.Duration, argv []string, sessionEnv map[string]string) (Result, error) {
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

	// Captured BEFORE the template exec so a hello racing the template's
	// return still satisfies the gate.
	startTime := time.Now()

	// Template contract: the spawn argv detaches the session and returns
	// promptly (like `zmx attach --detach`); Run blocking until the harness
	// exits would burn the hello deadline.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = rp.AbsPath
	cmd.Env = workerEnv(rp, desc, sessionEnv)
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("spawn template %q failed: %w", argv, err)
	}

	if err := chat.WaitForHello(driverKey, rp.SessionKey, startTime, deadline); err != nil {
		return Result{}, err
	}

	return Result{
		SessionKey:    rp.SessionKey,
		WorktreePath:  rp.AbsPath,
		MultiplexerID: rp.Branch,
	}, nil
}
