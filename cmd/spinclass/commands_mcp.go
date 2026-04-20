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

	"github.com/amarbel-llc/spinclass/internal/servelog"
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

			if err := servelog.Open(); err != nil {
				// Don't fail startup: logging is best-effort. Emit a line
				// to stderr and continue — stderr is captured by the MCP
				// client, so the user still sees it.
				fmt.Fprintf(os.Stderr, "spinclass serve: servelog open: %v\n", err)
			}
			defer func() { _ = servelog.Close() }()
			servelog.Infof("serve.start version=%s pid=%d", app.Version, os.Getpid())

			registry := server.NewToolRegistryV1()
			app.RegisterMCPToolsV1(registry)

			t := transport.NewStdio(os.Stdin, os.Stdout)

			// Safety net: once the transport has captured the JSON-RPC pipe,
			// reassign os.Stdout to os.Stderr. Any subprocess or print that
			// writes to os.Stdout from here on (e.g. a misbehaving hook)
			// lands on stderr instead of corrupting the protocol. See #27.
			os.Stdout = os.Stderr

			srv, err := server.New(t, server.Options{
				ServerName:    app.Name,
				ServerVersion: app.Version,
				Instructions:  "Git worktree session manager. Use the merge tool to merge a worktree branch into the default branch.",
				Tools:         registry,
			})
			if err != nil {
				return fmt.Errorf("creating server: %w", err)
			}

			servelog.Infof("serve.ready")
			err = srv.Run(sigCtx)
			if err != nil {
				servelog.Errorf("serve.exit err=%v", err)
			} else {
				servelog.Infof("serve.exit ok")
			}
			return err
		},
	})
}
