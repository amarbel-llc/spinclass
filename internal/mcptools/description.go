package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/protocol"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

func registerUpdateDescription(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "update-this-session-description",
		Title: "Update Session Description",
		Description: command.Description{
			Short: "Update the description of the current worktree session",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(false),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: []command.Param{
			{
				Name:        "description",
				Type:        command.String,
				Description: "New description for the session",
				Required:    true,
			},
		},
		Run: handleUpdateDescription,
	})
}

func handleUpdateDescription(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params struct {
		Description string `json:"description"`
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

	st, err := session.Read(repoPath, branch)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not read session state: %v", err)), nil
	}

	st.Description = params.Description
	if err := session.Write(*st); err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not write session state: %v", err)), nil
	}

	return command.TextResult(fmt.Sprintf("description updated to: %s", params.Description)), nil
}
