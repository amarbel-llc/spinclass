package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/server"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/transport"
)

func registerServeCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:   "serve",
		Hidden: true,
		Description: command.Description{
			Short: "Start MCP server on stdio",
			Long:  "Start a JSON-RPC MCP server on stdin/stdout. Intended to be launched by an MCP client such as Claude Code via .mcp.json.",
		},
		RunCLI: func(ctx context.Context, _ json.RawMessage) error {
			sigCtx, cancel := signal.NotifyContext(ctx, os.Interrupt)
			defer cancel()

			registry := server.NewToolRegistryV1()
			app.RegisterMCPToolsV1(registry)

			t := transport.NewStdio(os.Stdin, os.Stdout)
			srv, err := server.New(t, server.Options{
				ServerName:    app.Name,
				ServerVersion: app.Version,
				Instructions:  "Git worktree session manager. Use the merge tool to merge a worktree branch into the default branch.",
				Tools:         registry,
			})
			if err != nil {
				return fmt.Errorf("creating server: %w", err)
			}

			return srv.Run(sigCtx)
		},
	})
}
