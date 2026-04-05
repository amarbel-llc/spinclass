package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/protocol"
	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/merge"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

func registerMergeThisSession(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "merge-this-session",
		Title: "Merge This Session",
		Description: command.Description{
			Short: "Merge the current session's worktree into the default branch and clean up",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(false),
			DestructiveHint: protocol.BoolPtr(true),
			IdempotentHint:  protocol.BoolPtr(false),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: []command.Param{
			{
				Name:        "git_sync",
				Type:        command.Bool,
				Description: "Pull and push after merge (default false)",
			},
		},
		Run: handleMergeThisSession,
	})
}

func handleMergeThisSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params struct {
		GitSync bool `json:"git_sync"`
	}

	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}

	if !worktree.IsWorktree(cwd) {
		return command.TextErrorResult("not inside a worktree session"), nil
	}

	repoPath, err := git.CommonDir(cwd)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine repo path: %v", err)), nil
	}

	branch, err := git.BranchCurrent(cwd)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine current branch: %v", err)), nil
	}

	defaultBranch, err := merge.ResolveDefaultBranch(repoPath)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine default branch: %v", err)), nil
	}

	var buf bytes.Buffer
	mergeErr := merge.Resolved(
		executor.ShellExecutor{},
		&buf,
		nil,
		"tap",
		repoPath,
		cwd,
		branch,
		defaultBranch,
		params.GitSync,
		true,
		true,
	)

	if mergeErr != nil {
		return command.TextErrorResult(buf.String()), nil
	}

	return command.TextResult(buf.String()), nil
}
