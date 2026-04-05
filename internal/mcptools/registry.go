package mcptools

import "github.com/amarbel-llc/purse-first/libs/go-mcp/command"

func RegisterAll() *command.App {
	app := command.NewApp("spinclass", "MCP server for git worktree session management")
	app.Version = "0.1.0"

	registerMergeThisSession(app)
	registerUpdateDescription(app)

	return app
}
