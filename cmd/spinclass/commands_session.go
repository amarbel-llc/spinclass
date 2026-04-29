package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	spinclose "github.com/amarbel-llc/spinclass/internal/close"
	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/merge"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sessionpick"
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

	// `start-gh_pr` and `start-gh_issue` are registered dynamically from
	// sweatfile.GetDefault()'s baked-in [[start-commands]] entries via
	// registerPluginStartCommands(). See internal/sweatfile/sweatfile.go
	// defaultStartCommands().

	app.AddCommand(&command.Command{
		Name: "resume",
		Description: command.Description{
			Short: "Resume an existing worktree session",
			Long:  "Resume an existing worktree session. With no argument, auto-detects from the current working directory; if cwd isn't inside a tracked session, prompts interactively when stdin is a TTY or errors with the list of available session IDs otherwise. With one argument, resumes the session whose worktree directory name matches. Tab completion offers session IDs scoped to the current repo when run inside one, or all non-abandoned sessions otherwise (labels include the repo basename to disambiguate).",
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
			Long:  "Remove a worktree and its branch without merging into main. With no argument, closes the current worktree if cwd is inside one, otherwise prompts interactively when stdin is a TTY (or errors with the list of session IDs). With one argument, closes the named session; orphaned git worktrees without a spinclass state file are rejected with a hint to run `git worktree remove`. Prompts for confirmation if the branch has unintegrated commits or uncommitted changes; use --force to skip.",
		},
		Params: []command.Param{
			{Name: "target", Type: command.String, Description: "Target session ID (worktree directory name); auto-detects from cwd if omitted", Completer: completeWorktreeTargets},
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
}

// completeWorktreeTargets returns session IDs (worktree directory
// names) keyed to descriptive labels for tab completion. Inside a repo
// the list is scoped to that repo; outside any repo it includes every
// non-abandoned session and tags each label with its repo basename so
// duplicates across repos disambiguate. Output is sorted via
// session.SortStates so the active session shows up first.
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
	session.SortStates(sessions)

	result := make(map[string]string, len(sessions))
	for _, s := range sessions {
		id := filepath.Base(s.WorktreePath)
		label := s.Branch
		if s.Description != "" {
			label = fmt.Sprintf("%s — %s", s.Branch, s.Description)
		}
		if repoPath == "" {
			label = fmt.Sprintf("%s (%s)", label, filepath.Base(s.RepoPath))
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

	merged := hierarchy.Merged
	exec := executor.SessionExecutor{
		Entrypoint:  merged.SessionStart(),
		Description: resolvedPath.Description,
		Group:       merged.SessionGroup(),
	}

	return shop.Attach(
		os.Stdout,
		exec,
		resolvedPath,
		merged,
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
			state, err = sessionpick.Choose(repoPath, "resume")
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
		Entrypoint:  entrypoint,
		Description: state.Description,
		Group:       merged.SessionGroup(),
	}

	return shop.Attach(
		os.Stdout,
		exec,
		rp,
		merged,
		p.FormatOrDefault(),
		false,
		p.NoAttach,
		p.Verbose,
	)
}
