package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/spawn"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// forkDetachedParams is the shared parameter set of `sc fork --brief` and the
// `fork-session` MCP tool — both surfaces unmarshal into it and delegate to
// runForkDetached.
type forkDetachedParams struct {
	NewBranch    string `json:"new-branch"`
	Brief        string `json:"brief"`
	Description  string `json:"description"`
	HelloTimeout string `json:"hello-timeout"`
	Model        string `json:"model"`
}

// resolveForkSource resolves the fork SOURCE worktree from a directory,
// exactly as the classic `sc fork` always has: detect the repo, read the
// current branch, and require the standard .worktrees/<branch> layout. Both
// the create-only and detached fork paths share it, so the constraint (and
// its error text) cannot drift between them.
func resolveForkSource(sourceDir string) (worktree.ResolvedPath, error) {
	repoPath, err := worktree.DetectRepo(sourceDir)
	if err != nil {
		return worktree.ResolvedPath{}, err
	}

	currentBranch, err := git.BranchCurrent(sourceDir)
	if err != nil {
		return worktree.ResolvedPath{}, fmt.Errorf("could not determine current branch in %s: %w", sourceDir, err)
	}

	currentPath := filepath.Join(repoPath, worktree.WorktreesDir, currentBranch)
	if _, err := os.Stat(currentPath); os.IsNotExist(err) {
		return worktree.ResolvedPath{}, fmt.Errorf(
			"worktree path %s does not exist; fork requires a standard .worktrees layout",
			currentPath,
		)
	}

	return worktree.ResolvedPath{
		AbsPath:    currentPath,
		RepoPath:   repoPath,
		Branch:     currentBranch,
		SessionKey: filepath.Base(repoPath) + "/" + currentBranch,
	}, nil
}

// runForkDetached is the detached-fork flow shared by `sc fork --brief` and
// the `fork-session` MCP tool (FDR 0006): create the forked worktree the same
// way shop.Fork does (ForkName default, worktree.CreateFrom), then hand the
// existing worktree to spawn.LaunchExisting — state with spawned_by lineage,
// spawn-template exec, and the blocking chat-hello gate. Same-repo is the
// point here; only `sc spawn` rejects same-repo targets. Returns the launch
// result plus the driver key for the chat hint line.
//
// On a failure AFTER worktree creation (bad spawn templates, hello timeout)
// the forked worktree is intentionally left in place for inspection —
// `sc close` cleans it up — mirroring spawn-session's timeout contract.
func runForkDetached(source worktree.ResolvedPath, p forkDetachedParams) (spawn.Result, string, error) {
	if p.Brief == "" {
		return spawn.Result{}, "", errors.New("brief is required")
	}
	deadline, err := parseHelloTimeout(p.HelloTimeout)
	if err != nil {
		return spawn.Result{}, "", err
	}
	// Model alias validation is NOT done here: it's provider-conditional
	// (spawn.ValidateModelAlias), and the provider is only knowable once the
	// worker's sweatfile hierarchy is loaded inside spawn.LaunchExisting ->
	// renderSpawn -> SpliceModelFlag. Unlike the spawn path, that render
	// happens AFTER worktree.CreateFrom below, so — like any other bad
	// spawn-entry config — a bad model on the fork path can leave a forked
	// worktree behind; `sc close` cleans it up.

	home, err := os.UserHomeDir()
	if err != nil {
		return spawn.Result{}, "", fmt.Errorf("resolving home directory: %w", err)
	}

	// The driver identity is load-bearing, not informational: the worker's
	// hello is sent TO this key and the brief tells the worker to message it
	// back. No identity, no detached fork.
	driverKey, err := currentSessionKey()
	if err != nil {
		return spawn.Result{}, "", fmt.Errorf("resolving driver session key (the worker's hello target): %w", err)
	}

	if err := refuseRecursiveSpawn(); err != nil {
		return spawn.Result{}, "", err
	}

	newBranch := p.NewBranch
	if newBranch == "" {
		newBranch = worktree.ForkName(source.RepoPath, source.Branch)
	}
	newPath := filepath.Join(source.RepoPath, worktree.WorktreesDir, newBranch)

	if _, err := worktree.CreateFrom(source.RepoPath, source.AbsPath, newPath, newBranch); err != nil {
		return spawn.Result{}, "", fmt.Errorf("creating forked worktree: %w", err)
	}

	// Description deliberately travels only as LaunchExisting's desc param
	// (the channel that reaches the state write); rp.Description is unread
	// by the spawn package and setting it too would invite drift.
	rp := worktree.ResolvedPath{
		AbsPath:    newPath,
		RepoPath:   source.RepoPath,
		Branch:     newBranch,
		SessionKey: filepath.Base(source.RepoPath) + "/" + newBranch,
	}

	res, err := spawn.LaunchExisting(home, rp, driverKey, p.Brief, p.Description, p.Model, deadline)
	if err != nil {
		return spawn.Result{}, "", err
	}
	return res, driverKey, nil
}

// handleForkSession is the `fork-session` MCP tool handler. v1 mirrors the
// CLI's layout constraint: the caller must sit inside an sc worktree
// (.worktrees/<branch>). An implicit (main-checkout) session is rejected with
// a clear error — the CLI `sc fork` errors from a main checkout today too
// (its .worktrees/<branch> source check fails), so detached fork keeps parity
// rather than growing a main-checkout path the classic fork doesn't have.
func handleForkSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params forkDetachedParams
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	if params.Brief == "" {
		return command.TextErrorResult("brief is required"), nil
	}
	if _, err := parseHelloTimeout(params.HelloTimeout); err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	// Model alias validation happens later, inside runForkDetached's call to
	// spawn.LaunchExisting — see the comment there for why it can't be
	// checked this early (provider-conditional, needs the sweatfile).

	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}
	if !worktree.IsWorktree(cwd) {
		return command.TextErrorResult(
			"fork-session requires the calling session to be inside an sc worktree " +
				"(.worktrees/<branch>); detached fork from a main checkout is not " +
				"supported (v1 — `sc fork` has the same .worktrees-layout constraint). " +
				"Use spawn-session to launch a worker in a DIFFERENT repo.",
		), nil
	}

	source, err := resolveForkSource(cwd)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}

	res, driverKey, err := runForkDetached(source, params)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(spawnResultText(driverKey, res)), nil
}

// forkSessionParamList declares the fork-session MCP tool's parameters —
// the forkDetachedParams fields, with brief required.
func forkSessionParamList() []command.Param {
	return []command.Param{
		{
			Name:        "new-branch",
			Type:        command.String,
			Description: "Name for the forked branch (auto-generated as <current-branch>-N if omitted)",
		},
		{
			Name:        "brief",
			Type:        command.String,
			Required:    true,
			Description: "The worker's task brief — its ONLY context. Include everything it needs plus an explicit instruction to message you back via chat when done (your session key is its chat target).",
		},
		{
			Name:        "description",
			Type:        command.String,
			Description: "Session description for the worker (shows in `sc list`)",
		},
		{
			Name:        "hello-timeout",
			Type:        command.String,
			Description: "How long to wait for the worker's SessionStart hello, as a Go duration (e.g. \"90s\", \"3m\"). Default 60s. THE tuning lever when harness startup (cold nix cache, slow hosts) exceeds the default window.",
		},
		{
			Name:        "model",
			Type:        command.String,
			Description: "Model alias for the worker (sonnet, opus, haiku, fable). Spliced into the resolved spawn-entry's provider-args per [session-entry.model-flags] (default: {\"claude\": \"--model\"}). Omit to use the harness's own default.",
			Completer:   completeModelAliases,
		},
	}
}
