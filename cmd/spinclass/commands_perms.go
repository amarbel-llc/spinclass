package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/protocol"
	"github.com/amarbel-llc/spinclass/internal/perms"
)

func registerPermsCommands(app *command.App) {
	app.AddCommand(&command.Command{
		Name:   "perms-check",
		Hidden: true,
		Description: command.Description{
			Short: "Handle a Claude Code PermissionRequest hook",
		},
		RunCLI: func(_ context.Context, _ json.RawMessage) error {
			return perms.RunCheck(os.Stdin, os.Stdout, perms.TiersDir())
		},
	})

	app.AddCommand(&command.Command{
		Name: "perms-review",
		Description: command.Description{
			Short: "Interactively review new permissions from a session",
			Long:  "With --all, merges every session's tool-use log into a single review pass; only global promotion is allowed in that mode.",
		},
		Params: []command.Param{
			{Name: "worktree-path", Type: command.String, Description: "Worktree path to review (defaults to cwd)"},
			{Name: "worktree-dir", Type: command.String, Description: "Override worktree path (legacy alias)"},
			{Name: "dry-run", Type: command.Bool, Description: "Show what would change without writing"},
			{Name: "all", Type: command.Bool, Description: "Review across every session's tool-use log; only global promotion is allowed"},
			{Name: "include-builtin", Type: command.Bool, Description: "Include built-in tool rules (Bash, Read, etc.) in addition to MCP rules"},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				WorktreePath   string `json:"worktree-path"`
				WorktreeDir    string `json:"worktree-dir"`
				DryRun         bool   `json:"dry-run"`
				All            bool   `json:"all"`
				IncludeBuiltin bool   `json:"include-builtin"`
			}
			_ = json.Unmarshal(args, &p)

			if p.All {
				if p.WorktreePath != "" || p.WorktreeDir != "" {
					return fmt.Errorf("--all cannot be combined with worktree-path or --worktree-dir")
				}
				return perms.RunReviewEditorAll(p.DryRun, p.IncludeBuiltin)
			}

			worktreePath := p.WorktreeDir
			if worktreePath == "" {
				worktreePath = p.WorktreePath
			}
			if worktreePath == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				worktreePath = cwd
			}

			return perms.RunReview(worktreePath, p.DryRun, p.IncludeBuiltin)
		},
	})

	app.AddCommand(&command.Command{
		Name:  "perms-list",
		Title: "List Permission Tier Rules",
		Description: command.Description{
			Short: "List permission tier rules",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: []command.Param{
			{Name: "repo", Type: command.String, Description: "Show rules for a specific repo only"},
		},
		Run: func(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
			var p struct {
				Repo string `json:"repo"`
			}
			_ = json.Unmarshal(args, &p)
			out, err := perms.RunListString(p.Repo)
			if err != nil {
				return command.TextErrorResult(err.Error()), nil
			}
			return command.TextResult(out), nil
		},
	})

	app.AddCommand(&command.Command{
		Name: "perms-edit",
		Description: command.Description{
			Short: "Edit a permission tier file in $EDITOR",
		},
		Params: []command.Param{
			{Name: "global", Type: command.Bool, Description: "Edit the global tier file"},
			{Name: "repo", Type: command.String, Description: "Edit a repo-specific tier file"},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				Global bool   `json:"global"`
				Repo   string `json:"repo"`
			}
			_ = json.Unmarshal(args, &p)
			return perms.RunEdit(p.Global, p.Repo)
		},
	})
}
