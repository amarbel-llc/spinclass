package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/spinclass/internal/hooks"
)

func registerHookCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:   "hook",
		Hidden: true,
		Description: command.Description{
			Short: "Handle PreToolUse hook for worktree boundary enforcement",
		},
		RunCLI: func(_ context.Context, _ json.RawMessage) error {
			// Spinclass-specific deny logic (disallow-main-worktree etc.)
			// runs first; MapsTools-based denies via app.HandleHook are
			// reserved for future use.
			return hooks.Handle(os.Stdin, os.Stdout)
		},
	})
}
