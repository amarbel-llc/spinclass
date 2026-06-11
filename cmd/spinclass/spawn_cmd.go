package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/spawn"
)

// spawnParams is the shared parameter set of the `sc spawn` CLI command and
// the `spawn-session` MCP tool — both surfaces unmarshal into it and delegate
// to runSpawn.
type spawnParams struct {
	Repo         string `json:"repo"`
	Brief        string `json:"brief"`
	Issue        string `json:"issue"`
	Description  string `json:"description"`
	HelloTimeout string `json:"hello-timeout"`
}

// runSpawn is the shared spawn flow behind both surfaces (FDR 0006):
// validate params, resolve the driver identity (the worker's hello target
// and message-back address) and the target repo, optionally prepend a GitHub
// issue to the brief, then block in spawn.Launch until the worker's
// SessionStart hello or the deadline. Returns the launch result plus the
// driver key for the chat hint line.
func runSpawn(p spawnParams) (spawn.Result, string, error) {
	if p.Repo == "" {
		return spawn.Result{}, "", errors.New("repo is required")
	}
	if p.Brief == "" {
		return spawn.Result{}, "", errors.New("brief is required")
	}
	deadline, err := parseHelloTimeout(p.HelloTimeout)
	if err != nil {
		return spawn.Result{}, "", err
	}

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

	repoPath, err := spawn.ResolveRepo(home, p.Repo, driverRepoPath())
	if err != nil {
		return spawn.Result{}, "", err
	}

	brief := p.Brief
	if p.Issue != "" {
		brief, err = prependIssueToBrief(repoPath, p.Issue, brief)
		if err != nil {
			return spawn.Result{}, "", err
		}
	}

	res, err := spawn.Launch(home, repoPath, driverKey, brief, p.Description, deadline)
	if err != nil {
		return spawn.Result{}, "", err
	}
	return res, driverKey, nil
}

// parseHelloTimeout parses the shared hello-timeout param of the spawn and
// detached-fork surfaces. "" means 0, which the spawn package maps to
// DefaultHelloDeadline.
func parseHelloTimeout(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid hello-timeout %q (want a Go duration like \"90s\"): %w", s, err)
	}
	// A negative duration would silently fall through to the 60s default
	// inside the spawn package — reject it instead of surprising the user.
	if d < 0 {
		return 0, fmt.Errorf("hello-timeout must be positive, got %q", s)
	}
	return d, nil
}

// driverRepoPath best-effort resolves the DRIVER's repo path for
// spawn.ResolveRepo's same-repo rejection. git.CommonDir covers both session
// shapes: from a worktree it resolves the main checkout; from an implicit
// (main-checkout) session the cwd IS the repo. Anywhere git can't answer
// (not in a repo at all) it returns "" — an empty driver path never matches
// the always-absolute resolved target, so the rejection simply never fires.
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

// prependIssueToBrief fetches a GitHub issue from the RESOLVED target repo
// (cmd.Dir = repoPath so gh resolves against the worker repo's origin) and
// prepends "<title>\n\n<body>\n\n---\n\n" to the brief. Any failure — gh
// missing, API error, bad JSON — is a hard error: spawning a half-briefed
// worker is worse than not spawning.
func prependIssueToBrief(repoPath, issue, brief string) (string, error) {
	cmd := osexec.Command("gh", "issue", "view", issue, "--json", "title,body")
	cmd.Dir = repoPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			msg = ": " + msg
		}
		return "", fmt.Errorf("gh issue view %s in %s failed (refusing to spawn a half-briefed worker)%s: %w", issue, repoPath, msg, err)
	}
	var parsed struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return "", fmt.Errorf("parsing gh issue view %s output (refusing to spawn a half-briefed worker): %w", issue, err)
	}
	return parsed.Title + "\n\n" + parsed.Body + "\n\n---\n\n" + brief, nil
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

// handleSpawnSession is the `spawn-session` MCP tool handler.
func handleSpawnSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params spawnParams
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	res, driverKey, err := runSpawn(params)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(spawnResultText(driverKey, res)), nil
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
			Required:    true,
			Description: "Target repo: a dirname leaf-searched under $HOME/*/repos/<name>, or an explicit path (anything containing a path separator). Must be a main checkout of a DIFFERENT repo than the driver's — use a detached fork for a worker on a branch of this one.",
			Completer:   completeSpawnRepos,
		},
		{
			Name:        "brief",
			Type:        command.String,
			Required:    true,
			Description: "The worker's task brief — its ONLY context. Include everything it needs plus an explicit instruction to message you back via chat when done (your session key is its chat target).",
		},
		{
			Name:        "issue",
			Type:        command.String,
			Description: "GitHub issue number (or URL) in the TARGET repo: its title and body are fetched via `gh issue view` and prepended to the brief. Errors if gh is missing or the fetch fails — no half-briefed spawns.",
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
	}
}
