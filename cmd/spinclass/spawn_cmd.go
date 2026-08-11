package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/spawn"
)

// spawnParams is the shared parameter set of the `sc spawn` CLI command and
// the `spawn-session` MCP tool — both surfaces unmarshal into it and delegate
// to runSpawn.
type spawnParams struct {
	Repo         string `json:"repo"`
	Brief        string `json:"brief"`
	Description  string `json:"description"`
	HelloTimeout string `json:"hello-timeout"`
	Model        string `json:"model"`
}

// runSpawn is the shared spawn flow behind both surfaces (FDR 0006):
// validate params, resolve the driver identity (the worker's hello target
// and message-back address) and the target repo, then block in spawn.Launch
// until the worker's SessionStart hello or the deadline (parseHelloTimeout).
// Returns the launch result plus the driver key for the chat hint line. The
// brief is the worker's ONLY context (spinclass#258 removed the issue-prefill
// arg — the spawning agent references any issue in the brief and the worker
// fetches it).
func runSpawn(p spawnParams) (spawn.Result, string, error) {
	if p.Brief == "" {
		return spawn.Result{}, "", errors.New("brief is required")
	}
	deadline, err := parseHelloTimeout(p.HelloTimeout)
	if err != nil {
		return spawn.Result{}, "", err
	}
	// Model alias validation is NOT done here: it's provider-conditional
	// (spawn.ValidateModelAlias), and the provider is only knowable once the
	// worker's sweatfile hierarchy is loaded inside spawn.Launch ->
	// renderSpawn -> SpliceModelFlag. That still runs before shop.Create, so
	// a bad model never litters a worktree on this path.

	home, err := os.UserHomeDir()
	if err != nil {
		return spawn.Result{}, "", fmt.Errorf("resolving home directory: %w", err)
	}

	// The driver identity is load-bearing, not informational: the worker's
	// hello is sent TO this key and the brief tells the worker to message it
	// back. No identity, no spawn.
	driverKey, err := currentSessionKey()
	if err != nil {
		return spawn.Result{}, "", fmt.Errorf("resolving driver session key (the worker's hello target): %w", err)
	}

	repoPath, err := resolveSpawnRepo(home, p.Repo)
	if err != nil {
		return spawn.Result{}, "", err
	}

	res, err := spawn.Launch(home, repoPath, driverKey, p.Brief, p.Description, p.Model, deadline)
	if err != nil {
		return spawn.Result{}, "", err
	}
	return res, driverKey, nil
}

// resolveSpawnRepo resolves a spawn target repo (spinclass#262: repo is
// optional). An omitted repo — or one that resolves to the driver's own repo —
// means "this repo": a fresh worker off its default branch. A sibling
// dirname/path targets that repo. No same-repo refusal (fork-at-HEAD was
// dropped; a worker repositions its own branch if it needs a non-default start).
func resolveSpawnRepo(home, repo string) (string, error) {
	if repo == "" {
		repoPath := driverRepoPath()
		if repoPath == "" {
			return "", errors.New(
				"no repo specified and the current directory is not inside a git repo; pass a target repo dirname or path",
			)
		}
		return repoPath, nil
	}
	return spawn.ResolveRepo(home, repo)
}

// parseHelloTimeout parses the spawn surface's hello-timeout param. "" means 0,
// which the spawn package maps to DefaultHelloDeadline.
func parseHelloTimeout(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid hello-timeout %q (want a Go duration like \"90s\"): %w", s, err)
	}
	// An explicit non-positive duration ("0s", "-5s") would silently fall
	// through to the 60s default inside the spawn package — reject it
	// instead of surprising the user. Only "" means "use the default".
	if d <= 0 {
		return 0, fmt.Errorf("hello-timeout must be positive, got %q", s)
	}
	return d, nil
}

// driverRepoPath best-effort resolves the DRIVER's repo path — used as the
// spawn target when `repo` is omitted (spinclass#262: spawn in THIS repo).
// git.CommonDir covers both session shapes: from a worktree it resolves the
// main checkout; from an implicit (main-checkout) session the cwd IS the repo.
// Anywhere git can't answer (not in a repo at all) it returns "", which runSpawn
// turns into a "no repo specified and not inside a repo" error.
func driverRepoPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	repoPath, err := git.CommonDir(cwd)
	if err != nil {
		return ""
	}
	return repoPath
}

// spawnResultText renders the launch result for both surfaces: plain lines
// (spawn is not a merge/check command — mirror fork's plain print) plus the
// hint that the returned session key is the worker's chat address.
func spawnResultText(driverKey string, res spawn.Result) string {
	return fmt.Sprintf(
		"session_key: %s\nworktree_path: %s\nmultiplexer_id: %s\nworker will message %s via chat",
		res.SessionKey, res.WorktreePath, res.MultiplexerID, driverKey,
	)
}

// runSpawnCLI is the `sc spawn` RunCLI handler.
func runSpawnCLI(_ context.Context, args json.RawMessage) error {
	var p struct {
		globalArgs
		spawnParams
	}
	_ = json.Unmarshal(args, &p)

	res, driverKey, err := runSpawn(p.spawnParams)
	if err != nil {
		return err
	}
	fmt.Println(spawnResultText(driverKey, res))
	return nil
}

// handleSpawnSession is the `spawn-session` MCP tool handler. It is async-only
// by design (spinclass#266): under clown it returns the session key + a
// ringmaster job id immediately and delivers the worker's SessionStart hello as
// a job-wakeup (handleSpawnSessionAsync). Without clown there is no wake
// channel, so it falls back to the synchronous spawn (block on the hello and
// return the result inline) — the same path the `sc spawn` CLI uses.
func handleSpawnSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params spawnParams
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	if clown.Enabled() {
		return handleSpawnSessionAsync(params)
	}
	res, driverKey, err := runSpawn(params)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(spawnResultText(driverKey, res)), nil
}

// completeModelAliases offers the fixed set of known model aliases for
// tab completion / MCP client hinting.
func completeModelAliases() map[string]string {
	return map[string]string{
		"sonnet": "Claude Sonnet 5",
		"opus":   "Claude Opus 4.8",
		"haiku":  "Claude Haiku 4.5",
		"fable":  "Claude Fable 5",
	}
}

// completeSpawnRepos offers repo dirname leaves matching spawn.ResolveRepo's
// search pattern ($HOME/*/repos/*). Cheap glob only — no git checks at
// completion time; ResolveRepo validates the choice at execution.
func completeSpawnRepos() map[string]string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(home, "*", "repos", "*"))
	if err != nil {
		return nil
	}
	result := make(map[string]string, len(matches))
	for _, m := range matches {
		result[filepath.Base(m)] = m
	}
	return result
}

// spawnParamList declares the shared parameter schema of `sc spawn` and
// `spawn-session` — one definition so the two surfaces cannot drift.
func spawnParamList() []command.Param {
	return []command.Param{
		{
			Name:        "repo",
			Type:        command.String,
			Description: "Target repo (OPTIONAL, spinclass#262). Omit it — or name the current repo — to spawn a worker in THIS repo (a fresh worktree off its default branch). A different repo is a dirname leaf-searched under $HOME/*/repos/<name>, or an explicit path (anything containing a path separator); it must be a main checkout, not a worktree. The worker always starts fresh off the target's default branch with only the brief — if it needs a different starting point, it repositions its own branch.",
			Completer:   completeSpawnRepos,
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
			Description: "How long to wait for the worker's SessionStart hello, as a Go duration (e.g. \"90s\", \"3m\"). Default: 5m for the async spawn-session MCP tool (the wait is costless), 60s for the synchronous `sc spawn` CLI. The tuning lever when harness startup (cold nix cache, slow hosts) exceeds the default window.",
		},
		{
			Name:        "model",
			Type:        command.String,
			Description: "Model alias for the worker (sonnet, opus, haiku, fable). Spliced into the resolved spawn-entry's provider-args per [session-entry.model-flags] (default: {\"claude\": \"--model\"}). Omit to use the harness's own default.",
			Completer:   completeModelAliases,
		},
	}
}
