package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/protocol"
	"github.com/amarbel-llc/spinclass/internal/clean"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/pull"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/shop"
	"github.com/amarbel-llc/spinclass/internal/validate"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

func registerQueryCommands(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "list",
		Title: "List Spinclass Sessions",
		Description: command.Description{
			Short: "List tracked sessions",
			Long:  "List all tracked sessions from the state directory.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Run: func(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
			states, err := session.ListAll()
			if err != nil {
				return command.TextErrorResult(err.Error()), nil
			}
			var b strings.Builder
			for _, s := range states {
				fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", s.SessionKey, s.ResolveState(), s.WorktreePath, s.Description)
			}
			return command.TextResult(b.String()), nil
		},
	})

	app.AddCommand(&command.Command{
		Name:  "validate",
		Title: "Validate Sweatfile Hierarchy",
		Description: command.Description{
			Short: "Validate the sweatfile hierarchy",
			Long:  "Walk the sweatfile hierarchy from PWD and validate each file for structural and semantic correctness. Outputs TAP-14 with subtests.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Run: func(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
			cwd, err := os.Getwd()
			if err != nil {
				return command.TextErrorResult(err.Error()), nil
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return command.TextErrorResult(err.Error()), nil
			}
			out, exitCode := validate.RunString(home, cwd)
			if exitCode != 0 {
				return &command.Result{Text: out, IsErr: true}, nil
			}
			return command.TextResult(out), nil
		},
	})

	app.AddCommand(&command.Command{
		Name: "clean",
		Description: command.Description{
			Short: "Remove merged worktrees",
			Long:  "Scan all worktrees, identify those whose branches are fully merged into the main branch, and remove them. Use -i to interactively handle dirty worktrees.",
		},
		Params: []command.Param{
			{
				Name:        "interactive",
				Short:       'i',
				Type:        command.Bool,
				Description: "Interactively discard changes in dirty merged worktrees",
			},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				Interactive bool `json:"interactive"`
			}
			_ = json.Unmarshal(args, &p)

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return clean.Run(cwd, p.Interactive, p.FormatOrDefault())
		},
	})

	app.AddCommand(&command.Command{
		Name: "pull",
		Description: command.Description{
			Short: "Pull repos and rebase worktrees",
			Long:  "Pull all clean repos, then rebase all clean worktrees onto their repo's default branch. Use -d to include dirty repos and worktrees.",
		},
		Params: []command.Param{
			{
				Name:        "dirty",
				Short:       'd',
				Type:        command.Bool,
				Description: "Include dirty repos and worktrees",
			},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				Dirty bool `json:"dirty"`
			}
			_ = json.Unmarshal(args, &p)

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return pull.Run(cwd, p.Dirty, p.FormatOrDefault())
		},
	})

	app.AddCommand(&command.Command{
		Name: "fork",
		Description: command.Description{
			Short: "Fork current worktree into a new branch",
			Long:  "Create a new worktree branched from the current worktree's HEAD. If new-branch is omitted, a name is auto-generated as <current-branch>-N. Resolves the source worktree from the current directory or --from flag. Does not attach to the new session.",
		},
		Params: []command.Param{
			{Name: "new-branch", Type: command.String, Description: "Name for the forked branch (auto-generated if omitted)"},
			{Name: "from", Type: command.String, Description: "Source worktree directory to fork from", Completer: completeWorktreeTargets},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				NewBranch string `json:"new-branch"`
				From      string `json:"from"`
			}
			_ = json.Unmarshal(args, &p)

			sourceDir := p.From
			if sourceDir == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				sourceDir = cwd
			}

			repoPath, err := worktree.DetectRepo(sourceDir)
			if err != nil {
				return err
			}

			currentBranch, err := git.BranchCurrent(sourceDir)
			if err != nil {
				return fmt.Errorf("could not determine current branch in %s: %w", sourceDir, err)
			}

			currentPath := filepath.Join(repoPath, worktree.WorktreesDir, currentBranch)
			if _, err := os.Stat(currentPath); os.IsNotExist(err) {
				return fmt.Errorf(
					"worktree path %s does not exist; fork requires a standard .worktrees layout",
					currentPath,
				)
			}

			rp := worktree.ResolvedPath{
				AbsPath:    currentPath,
				RepoPath:   repoPath,
				Branch:     currentBranch,
				SessionKey: filepath.Base(repoPath) + "/" + currentBranch,
			}

			return shop.Fork(os.Stdout, rp, p.NewBranch, p.FormatOrDefault())
		},
	})

	app.AddCommand(&command.Command{
		Name: "update-description",
		Description: command.Description{
			Short: "Update the description of a session",
			Long:  "Update the freeform description of an existing session. With --id, targets a specific worktree by directory name. Without --id, auto-detects from the current working directory. Multi-word descriptions must be quoted.",
		},
		Params: []command.Param{
			{Name: "description", Type: command.String, Description: "New description (quote multi-word strings)", Required: true},
			{Name: "id", Type: command.String, Description: "Worktree ID to update (auto-detects from cwd if omitted)", Completer: completeWorktreeTargets},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				Description string `json:"description"`
				ID          string `json:"id"`
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
			}
			if err != nil {
				return err
			}

			state.Description = p.Description
			return session.Write(*state)
		},
	})
}
