package main

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
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

// registerMCPOnlyCommands registers commands that only make sense as MCP
// tools (not user-facing CLI commands). They are Hidden so they don't show
// up in help, but they're still exposed via RegisterMCPToolsV1 because the
// hidden filter only excludes them from CLI/help output, not MCP.
//
// Wait — actually VisibleCommands excludes Hidden from MCP too. So these
// commands are NOT marked Hidden; instead they have a recognizable
// "session-tool" prefix so users who run `sc help` understand they're
// agent-facing helpers.
func registerMCPOnlyCommands(app *command.App) {
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
			{Name: "git_sync", Type: command.Bool, Description: "Pull and push after merge (default false)"},
		},
		Run: handleMergeThisSession,
	})

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
			{Name: "description", Type: command.String, Description: "New description for the session", Required: true},
		},
		Run: handleUpdateDescription,
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
