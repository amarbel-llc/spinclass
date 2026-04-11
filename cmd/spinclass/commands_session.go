package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	spinclose "github.com/amarbel-llc/spinclass/internal/close"
	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/merge"
	"github.com/amarbel-llc/spinclass/internal/pr"
	"github.com/amarbel-llc/spinclass/internal/prompt"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/shop"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

func registerSessionCommands(app *command.App) {
	app.AddCommand(&command.Command{
		Name: "start",
		Description: command.Description{
			Short: "Create and start a new worktree session",
			Long:  "Create a new worktree with a random branch name and start a session. The optional description is a freeform string used to label the session; quote multi-word descriptions.",
		},
		Params: []command.Param{
			{Name: "description", Type: command.String, Description: "Freeform session description (quote multi-word strings)"},
			{Name: "merge-on-close", Type: command.Bool, Description: "Auto-merge worktree into default branch on session close"},
			{Name: "no-attach", Type: command.Bool, Description: "Create worktree but skip attaching"},
		},
		RunCLI: runStart,
	})

	app.AddCommand(&command.Command{
		Name: "start-gh_pr",
		Description: command.Description{
			Short: "Start a session from a GitHub pull request",
			Long:  "Create a new worktree session from an existing GitHub PR. Fetches the PR branch and sets up session context with PR metadata.",
		},
		Params: []command.Param{
			{Name: "pr", Type: command.String, Description: "PR number or GitHub URL", Required: true, Completer: completeGHPRs},
			{Name: "description", Type: command.String, Description: "Override the PR title as session description"},
			{Name: "merge-on-close", Type: command.Bool, Description: "Auto-merge worktree into default branch on session close"},
			{Name: "no-attach", Type: command.Bool, Description: "Create worktree but skip attaching"},
		},
		RunCLI: runStartGHPR,
	})

	// `start-gh_issue` is registered dynamically from
	// sweatfile.GetDefault()'s baked-in [[start-commands]] entry via
	// registerPluginStartCommands(). See internal/sweatfile/sweatfile.go
	// defaultStartCommands().

	app.AddCommand(&command.Command{
		Name: "resume",
		Description: command.Description{
			Short: "Resume an existing worktree session",
			Long:  "Resume an existing worktree session. With no arguments, auto-detects the session from the current working directory. With one argument, resumes the session identified by the worktree directory name.",
		},
		Params: []command.Param{
			{Name: "id", Type: command.String, Description: "Session ID (worktree directory name); auto-detects from cwd if omitted", Completer: completeWorktreeTargets},
			{Name: "no-attach", Type: command.Bool, Description: "Find session but skip attaching"},
		},
		RunCLI: runResume,
	})

	app.AddCommand(&command.Command{
		Name: "merge",
		Description: command.Description{
			Short: "Merge a worktree into main",
			Long:  "Merge a worktree branch into the main repo with --ff-only and remove the worktree. When run from inside a worktree, merges that worktree. When run from the main repo, specify a target or choose interactively.",
		},
		Params: []command.Param{
			{Name: "target", Type: command.String, Description: "Target worktree to merge (interactive selection if omitted)", Completer: completeWorktreeTargets},
			{Name: "git-sync", Type: command.Bool, Description: "Pull and push after merge"},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				Target  string `json:"target"`
				GitSync bool   `json:"git-sync"`
			}
			_ = json.Unmarshal(args, &p)

			return merge.Run(executor.ShellExecutor{}, p.FormatOrDefault(), p.Target, p.GitSync, p.Verbose)
		},
	})

	app.AddCommand(&command.Command{
		Name: "close",
		Description: command.Description{
			Short: "Close a session without merging",
			Long:  "Remove a worktree and its branch without merging into main. Prompts for confirmation if the branch has not been pushed upstream. Use --force to skip.",
		},
		Params: []command.Param{
			{Name: "target", Type: command.String, Description: "Target worktree to close", Completer: completeWorktreeTargets},
			{Name: "force", Short: 'f', Type: command.Bool, Description: "Skip confirmation for unpushed branches"},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				Target string `json:"target"`
				Force  bool   `json:"force"`
			}
			_ = json.Unmarshal(args, &p)

			return spinclose.Run(os.Stdout, p.Target, p.Force, p.FormatOrDefault())
		},
	})

	app.AddCommand(&command.Command{
		Name:            "exec-claude",
		Hidden:          true,
		PassthroughArgs: true,
		Description: command.Description{
			Short: "Execute claude after applying sweatfile settings",
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				Args []string `json:"args"`
			}
			_ = json.Unmarshal(args, &p)

			hierarchy, err := sweatfile.LoadDefaultHierarchy()
			if err != nil {
				return err
			}
			return hierarchy.Merged.ExecClaude(p.Args...)
		},
	})
}

func completeGHPRs() map[string]string {
	out, err := exec.Command(
		"gh", "pr", "list", "--json", "number,title", "--limit", "20",
	).Output()
	if err != nil {
		return nil
	}

	var prs []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
	if json.Unmarshal(out, &prs) != nil {
		return nil
	}

	result := make(map[string]string, len(prs))
	for _, p := range prs {
		result[fmt.Sprintf("%d", p.Number)] = p.Title
	}
	return result
}

func completeWorktreeTargets() map[string]string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}

	var sessions []session.State
	repoPath, err := worktree.DetectRepo(cwd)
	if err == nil {
		sessions, _ = session.ListForRepo(repoPath)
	} else {
		// Outside a git repo: show all non-abandoned sessions.
		all, err := session.ListAll()
		if err != nil {
			return nil
		}
		for _, s := range all {
			if s.ResolveState() != session.StateAbandoned {
				sessions = append(sessions, s)
			}
		}
	}

	result := make(map[string]string, len(sessions))
	for _, s := range sessions {
		id := filepath.Base(s.WorktreePath)
		label := s.Branch
		if s.Description != "" {
			label = fmt.Sprintf("%s — %s", s.Branch, s.Description)
		}
		result[id] = label
	}
	return result
}

type startArgs struct {
	globalArgs
	Description  string `json:"description"`
	MergeOnClose bool   `json:"merge-on-close"`
	NoAttach     bool   `json:"no-attach"`
}

func attachSession(resolvedPath worktree.ResolvedPath, args startArgs) error {
	repoPath := resolvedPath.RepoPath

	hierarchy, err := sweatfile.LoadWorktreeHierarchy(
		os.Getenv("HOME"), repoPath, resolvedPath.AbsPath,
	)
	if err != nil {
		hierarchy, err = sweatfile.LoadHierarchy(os.Getenv("HOME"), repoPath)
		if err != nil {
			return err
		}
	}

	exec := executor.SessionExecutor{
		Entrypoint:  hierarchy.Merged.SessionStart(),
		Description: resolvedPath.Description,
	}

	return shop.Attach(
		os.Stdout,
		exec,
		resolvedPath,
		args.FormatOrDefault(),
		args.MergeOnClose,
		args.NoAttach,
		args.Verbose,
	)
}

func runStart(_ context.Context, args json.RawMessage) error {
	var p startArgs
	_ = json.Unmarshal(args, &p)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	repoPath, err := worktree.DetectRepo(cwd)
	if err != nil {
		return err
	}

	var descArgs []string
	if p.Description != "" {
		descArgs = []string{p.Description}
	}

	resolvedPath, err := worktree.ResolvePath(repoPath, descArgs)
	if err != nil {
		return err
	}

	return attachSession(resolvedPath, p)
}

func runStartGHPR(_ context.Context, args json.RawMessage) error {
	var p struct {
		startArgs
		PR string `json:"pr"`
	}
	_ = json.Unmarshal(args, &p)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	repoPath, err := worktree.DetectRepo(cwd)
	if err != nil {
		return err
	}

	prInfo, err := pr.Resolve(p.PR, repoPath)
	if err != nil {
		return err
	}

	branch := prInfo.HeadRefName

	if !git.BranchExists(repoPath, branch) {
		if _, err := git.Run(repoPath, "fetch", "origin", branch); err != nil {
			return fmt.Errorf("fetching PR branch %q: %w", branch, err)
		}
	}

	absPath := filepath.Join(repoPath, worktree.WorktreesDir, branch)
	repoDirname := filepath.Base(repoPath)

	description := fmt.Sprintf("%s (#%d)", prInfo.Title, prInfo.Number)
	if p.Description != "" {
		description = p.Description
	}

	resolvedPath := worktree.ResolvedPath{
		AbsPath:        absPath,
		RepoPath:       repoPath,
		SessionKey:     repoDirname + "/" + branch,
		Branch:         branch,
		Description:    description,
		ExistingBranch: branch,
	}

	if prData, prErr := prompt.FetchPR(p.PR, repoPath); prErr == nil {
		resolvedPath.PR = &prData
	}

	return attachSession(resolvedPath, p.startArgs)
}

func runResume(_ context.Context, args json.RawMessage) error {
	var p struct {
		globalArgs
		ID       string `json:"id"`
		NoAttach bool   `json:"no-attach"`
	}
	_ = json.Unmarshal(args, &p)

	var state *session.State
	var err error

	if p.ID != "" {
		state, err = session.FindByID(p.ID)
	} else {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return cwdErr
		}
		state, err = session.FindByWorktreePath(cwd)
		if err != nil {
			repoPath, repoErr := worktree.DetectRepo(cwd)
			if repoErr != nil {
				return err
			}
			state, err = chooseSession(repoPath)
		}
	}
	if err != nil {
		return err
	}

	hierarchy, err := sweatfile.LoadWorktreeHierarchy(
		os.Getenv("HOME"), state.RepoPath, state.WorktreePath,
	)
	if err != nil {
		hierarchy, err = sweatfile.LoadHierarchy(os.Getenv("HOME"), state.RepoPath)
		if err != nil {
			return err
		}
	}

	merged := hierarchy.Merged
	entrypoint := merged.SessionStart()
	if resume := merged.SessionResume(); resume != nil {
		entrypoint = resume
	}

	rp := worktree.ResolvedPath{
		AbsPath:     state.WorktreePath,
		RepoPath:    state.RepoPath,
		SessionKey:  state.SessionKey,
		Branch:      state.Branch,
		Description: state.Description,
	}

	exec := executor.SessionExecutor{
		Entrypoint: entrypoint,
	}

	return shop.Attach(
		os.Stdout,
		exec,
		rp,
		p.FormatOrDefault(),
		false,
		p.NoAttach,
		p.Verbose,
	)
}

func chooseSession(repoPath string) (*session.State, error) {
	sessions, err := session.ListForRepo(repoPath)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("no sessions found for %s", filepath.Base(repoPath))
	}

	interactive := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
	if !interactive {
		var ids []string
		for _, s := range sessions {
			ids = append(ids, filepath.Base(s.WorktreePath))
		}
		return nil, fmt.Errorf(
			"no session found for current directory; available sessions: %s\nUse: spinclass resume <id>",
			joinIDs(ids),
		)
	}

	options := make([]huh.Option[int], len(sessions))
	for i, s := range sessions {
		label := fmt.Sprintf("%s [%s]", s.Branch, s.ResolveState())
		if s.Description != "" {
			label = fmt.Sprintf("%s — %s [%s]", s.Branch, s.Description, s.ResolveState())
		}
		options[i] = huh.NewOption(label, i)
	}

	var selected int
	err = huh.NewSelect[int]().
		Title("Select session to resume").
		Options(options...).
		Value(&selected).
		Run()
	if err != nil {
		return nil, fmt.Errorf("session selection cancelled")
	}

	return &sessions[selected], nil
}

func joinIDs(ids []string) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ", "
		}
		out += id
	}
	return out
}
