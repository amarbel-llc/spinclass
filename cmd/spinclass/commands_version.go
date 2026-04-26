package main

import (
	"context"
	"encoding/json"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/protocol"
)

func registerVersionCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "version",
		Title: "Print Spinclass Version",
		Description: command.Description{
			Short: "Print the burnt-in version and commit",
			Long:  "Print the version and commit SHA injected at build time as `<version>+<commit>`. A devshell `go build` reports `dev+unknown`; a `nix build` reports the flake's spinclassVersion plus shortRev.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Run: func(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
			return command.TextResult(version + "+" + commit + "\n"), nil
		},
	})
}
