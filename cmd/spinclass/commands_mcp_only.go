package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"time"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/protocol"
	"github.com/amarbel-llc/spinclass/internal/check"
	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/merge"
	"github.com/amarbel-llc/spinclass/internal/servelog"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

// wrapMCPHandler adds entry/exit logging and panic recovery around an MCP
// tool handler. A panic becomes a TextErrorResult instead of a dead server
// (which in stdio mode means the MCP client sees the connection close and
// every subsequent tool call fails with `No such tool available: …`).
func wrapMCPHandler(
	name string,
	fn func(ctx context.Context, args json.RawMessage, p command.Prompter) (*command.Result, error),
) func(ctx context.Context, args json.RawMessage, p command.Prompter) (*command.Result, error) {
	return func(ctx context.Context, args json.RawMessage, p command.Prompter) (res *command.Result, err error) {
		start := time.Now()
		servelog.Infof("mcp.handler.enter name=%s args_size=%d", name, len(args))

		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				servelog.Errorf("mcp.handler.panic name=%s recovered=%v\n%s", name, r, stack)
				logPath := servelog.Path()
				msg := fmt.Sprintf("spinclass handler %q panicked: %v", name, r)
				if logPath != "" {
					msg += fmt.Sprintf(" (see %s)", logPath)
				}
				res = command.TextErrorResult(msg)
				err = nil
			}
			servelog.Infof("mcp.handler.exit name=%s dur=%s", name, time.Since(start))
		}()

		return fn(ctx, args, p)
	}
}

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
			Short: "Merge the current session's worktree into the default branch and clean up. A non-error return means the merge (and push, if git_sync) succeeded; the output payload is informational and does not need to be read or parsed to confirm success. Only inspect output on error.",
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
		Run: wrapMCPHandler("merge-this-session", handleMergeThisSession),
	})

	app.AddCommand(&command.Command{
		Name:  "check-this-session",
		Title: "Check This Session",
		Description: command.Description{
			Short: "Run the configured [hooks].pre-merge command in the current worktree without merging. This is the agent-CI surface; safe to call repeatedly. Returns non-zero / error if the hook fails.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(false),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(false),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: []command.Param{},
		Run:    wrapMCPHandler("check-this-session", handleCheckThisSession),
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
		Run: wrapMCPHandler("update-this-session-description", handleUpdateDescription),
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

func handleCheckThisSession(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}

	var buf bytes.Buffer
	if err := check.Run(&buf, "tap", cwd, false); err != nil {
		text := buf.String()
		if text == "" {
			text = err.Error()
		}
		return command.TextErrorResult(text), nil
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
