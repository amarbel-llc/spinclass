package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	spinclose "code.linenisgreat.com/spinclass/internal/close"
	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/job"
	"code.linenisgreat.com/spinclass/internal/servelog"
	"code.linenisgreat.com/spinclass/internal/spawn"
)

// AsyncSpawnHelloDeadline is the default hello-timeout for the async
// spawn-session MCP tool (spinclass#266). Much longer than the sync
// spawn.DefaultHelloDeadline (60s, kept for `sc spawn`) because async makes the
// wait costless — the caller got the session key + job id and moved on — so a
// generous window just stops cold nix-cache boots from failing spawns. Tuning
// lever: raise if cold boots still time out, lower if wedged boots hang a job
// too long.
const AsyncSpawnHelloDeadline = 5 * time.Minute

// spawnLogActiveWindow bounds "recently written" for the reap-if-dead liveness
// heuristic (spinclass#266 decision 1): at a hello timeout, a worker whose
// spawn.log was touched within this window is treated as still-booting (kept),
// else as dead (auto-reaped). Tuning lever: widen if live-but-idle workers are
// wrongly reaped, narrow (or switch to a clown-presence query) if dead workers
// linger.
const spawnLogActiveWindow = 30 * time.Second

// handleSpawnSessionAsync is the async spawn-session path (spinclass#266), used
// when serve runs under clown. It runs the synchronous prefix (create worktree +
// exec the detached entry) inline — so the session key is known — then hands the
// hello wait to a background goroutine that delivers the outcome as a clown wake,
// returning the session key + ringmaster job id immediately so the caller is
// never pinned on a slow cold boot.
func handleSpawnSessionAsync(params spawnParams) (*command.Result, error) {
	if params.Brief == "" {
		return command.TextErrorResult("brief is required"), nil
	}
	deadline, err := parseHelloTimeout(params.HelloTimeout)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	if deadline == 0 {
		deadline = AsyncSpawnHelloDeadline // async default (sync `sc spawn` keeps 60s)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("resolving home directory: %v", err)), nil
	}
	driverKey, err := currentSessionKey()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("resolving driver session key (the worker's hello target): %v", err)), nil
	}
	repoPath, err := resolveSpawnRepo(home, params.Repo)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}

	// Synchronous prefix: create the worktree + exec the detached entry. After
	// this the session key is known; the hello has NOT been awaited.
	pending, err := spawn.LaunchDetached(home, repoPath, driverKey, params.Brief, params.Description, params.Model)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}

	// Allocate the clown job that carries the handshake wake.
	jobID, cerr := clown.StartJob(context.Background(), "spawn", clown.Source)
	if cerr != nil {
		// The worker is already booting; without a wake channel, fall back to an
		// inline wait rather than orphaning it (same degrade shape as no-clown).
		servelog.Errorf("spawn-async: clown StartJob failed, waiting for hello inline: %v", cerr)
		res, werr := spawn.WaitHello(pending, deadline)
		if werr != nil {
			return command.TextErrorResult(werr.Error()), nil
		}
		return command.TextResult(spawnResultText(driverKey, res)), nil
	}

	go awaitSpawnHello(pending, driverKey, deadline, jobID)

	return command.TextResult(asyncSpawnResultText(pending, driverKey, jobID, deadline)), nil
}

// awaitSpawnHello runs in a background goroutine: block on the worker's hello,
// then emit the terminal clown wake. On timeout it applies the reap-if-dead
// policy — reap the never-helloed session only when it looks dead, else keep and
// name it (spinclass#266 decision 1). The goroutine outlives the tool call
// (serve is long-lived), mirroring the async merge goroutine.
func awaitSpawnHello(pending spawn.Pending, driverKey string, deadline time.Duration, jobID string) {
	ctx := context.Background()

	if res, err := spawn.WaitHello(pending, deadline); err == nil {
		msg := fmt.Sprintf("worker %s is up; it will message %s via chat", res.SessionKey, driverKey)
		if ferr := clown.FinishJob(ctx, jobID, job.StatusSucceeded, msg, ""); ferr != nil {
			servelog.Errorf("spawn-async: FinishJob(succeeded) for %s failed: %v", pending.SessionKey, ferr)
		}
		return
	}

	// Hello timeout (or handshake error): apply the reap-if-dead policy.
	msg := spawnTimeoutOutcome(pending, deadline)
	if ferr := clown.FinishJob(ctx, jobID, job.StatusAborted, msg, ""); ferr != nil {
		servelog.Errorf("spawn-async: FinishJob(aborted) for %s failed: %v", pending.SessionKey, ferr)
	}
}

// spawnTimeoutOutcome decides what to do with a never-helloed worker at timeout
// and returns the wake message. spawn.log activity within spawnLogActiveWindow
// means the worker is still booting (keep + name it); otherwise it looks dead
// and is auto-reaped (no-force, clean by construction — pre-hello, no commits).
func spawnTimeoutOutcome(pending spawn.Pending, deadline time.Duration) string {
	d := deadline.Round(time.Second)
	if spawn.SpawnLogActiveWithin(pending.WorktreePath, spawnLogActiveWindow) {
		return fmt.Sprintf(
			"spawn hello timed out after %s, but the worker's spawn.log still shows activity — likely a slow cold boot. Session %q is dangling: wait for a late hello, or reap it with close-child-session.",
			d, pending.SessionKey,
		)
	}
	// Looks dead — reap. No-force: a pre-hello worker has no commits and a clean
	// tree, so it meets close-child-session's own no-force condition by
	// construction; a surprise dirty/unmerged state makes RunResolved refuse and
	// we report the session as dangling instead of discarding anything.
	if rerr := spinclose.RunResolved(io.Discard, pending.RepoPath, pending.WorktreePath, pending.Branch, false, nil, "tap"); rerr != nil {
		return fmt.Sprintf(
			"spawn hello timed out after %s and the worker looked dead, but auto-reap failed (%v). Session %q is dangling — reap it with close-child-session.",
			d, rerr, pending.SessionKey,
		)
	}
	return fmt.Sprintf(
		"spawn hello timed out after %s; the worker never helloed and looked dead, so session %q was reaped (no work existed). Re-spawn, optionally with a longer hello-timeout.",
		d, pending.SessionKey,
	)
}

// asyncSpawnResultText is the immediate tool result for an async spawn: the
// session key (the worker's chat address), worktree, and the ringmaster job id
// that carries the hello outcome. It states the hello is delivered as a wake so
// the caller ends its turn instead of polling.
func asyncSpawnResultText(pending spawn.Pending, driverKey, jobID string, deadline time.Duration) string {
	return fmt.Sprintf(
		"spawning worker %s (worktree %s) — returned immediately. Its SessionStart hello is delivered as a job-wakeup on ringmaster job %q (hello-timeout %s): end your turn and let the wake arrive, do not poll. On hello the worker is up and will message %s via chat. On timeout, if the worker looks dead it is auto-reaped (nothing of value existed), else the wake names the dangling session to reap with close-child-session. Inspect via ringmaster's job_status/job_read with that id.",
		pending.SessionKey, pending.WorktreePath, jobID, deadline.Round(time.Second), driverKey,
	)
}
