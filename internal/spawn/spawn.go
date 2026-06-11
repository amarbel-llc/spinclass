package spawn

import (
	"fmt"
	"io"
	"os/exec"
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

	if err := shop.Create(io.Discard, rp, false, "", nil); err != nil {
		return Result{}, fmt.Errorf("creating worker worktree: %w", err)
	}

	return LaunchExisting(home, rp, driverKey, brief, desc, deadline)
}

// LaunchExisting runs the post-worktree tail of Launch over an existing
// worktree: write the worker's session state (spawned_by lineage), render
// the worker repo's spawn/spawn-entry templates, exec the spawn argv, and
// block on the chat hello. Task 7's detached fork reuses it over a
// worktree.CreateFrom-produced worktree.
func LaunchExisting(home string, rp worktree.ResolvedPath, driverKey, brief, desc string, deadline time.Duration) (Result, error) {
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

	// The WORKER repo's hierarchy decides the templates — its multiplexer
	// and harness, not the driver's.
	hierarchy, err := sweatfileio.LoadWorktreeHierarchy(home, rp.RepoPath, rp.AbsPath)
	if err != nil {
		return Result{}, fmt.Errorf("loading worker sweatfile hierarchy: %w", err)
	}
	merged := hierarchy.Merged

	entry := SubstituteEntry(merged.SessionSpawnEntry(), brief, rp.AbsPath)
	argv, err := SubstituteSpawn(merged.SessionSpawn(), rp.Branch, rp.AbsPath, entry)
	if err != nil {
		return Result{}, err
	}

	// Captured BEFORE the template exec so a hello racing the template's
	// return still satisfies the gate.
	startTime := time.Now()

	// Template contract: the spawn argv detaches the session and returns
	// promptly (like `zmx run`); Run blocking until the harness exits would
	// burn the hello deadline.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = rp.AbsPath
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
