package main

import (
	"embed"
	"encoding/json"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
)

//go:embed doc/spinclass-start-commands.7
var extraManpages embed.FS

// version is set at link time via -ldflags "-X main.version=...".
var version = "dev"

// buildApp constructs the spinclass command.App with global flags and all
// registered subcommands. It is called from main and from the serve
// subcommand to share a single source of truth for both CLI and MCP
// surfaces.
func buildApp() *command.App {
	app := command.NewApp("spinclass", "Shell-agnostic git worktree session manager")
	app.Version = version
	app.Aliases = []string{"sc"}
	app.Description.Long = "Manages git worktree session lifecycles: create, attach via configurable session entrypoints, rebase/merge back to main, and clean up. Aliased as `sc`."
	app.PluginAuthor = "amarbel-llc"
	app.PluginDescription = "Git worktree session manager with sweatfile-driven configuration"
	app.MCPArgs = []string{"serve"}
	app.ExtraManpages = []command.ManpageFile{
		{
			Source:  extraManpages,
			Path:    "doc/spinclass-start-commands.7",
			Section: 7,
			Name:    "spinclass-start-commands.7",
		},
	}

	app.Params = []command.Param{
		{
			Name:        "format",
			Type:        command.String,
			Description: "Output format: tap or table (default: tap)",
		},
		{
			Name:        "verbose",
			Short:       'v',
			Type:        command.Bool,
			Description: "Show YAML diagnostics on TAP-14 output",
		},
	}

	registerQueryCommands(app)
	registerSessionCommands(app)
	registerPermsCommands(app)
	registerHookCommand(app)
	registerServeCommand(app)
	registerMCPOnlyCommands(app)
	registerGenerateArtifactsCommand(app)
	// Plugin start-* commands must register AFTER the built-in session
	// commands so the built-ins shadow any config entries of the same name
	// via the `GetCommand` collision guard.
	registerPluginStartCommands(app)

	return app
}

// globalArgs is the subset of global flags exposed to every command handler.
// Handlers unmarshal their args JSON into a struct embedding globalArgs (or
// just into globalArgs itself when no command-specific params exist) to
// access --format and --verbose.
type globalArgs struct {
	Format  string `json:"format"`
	Verbose bool   `json:"verbose"`
}

// FormatOrDefault returns the configured format, defaulting to "tap" when
// the user did not pass --format.
func (g globalArgs) FormatOrDefault() string {
	if g.Format == "" {
		return "tap"
	}
	return g.Format
}

// parseGlobalArgs unmarshals the args JSON into a globalArgs. Errors are
// silently ignored: missing or unparseable globals fall back to defaults.
func parseGlobalArgs(args json.RawMessage) globalArgs {
	var g globalArgs
	_ = json.Unmarshal(args, &g)
	return g
}
