package main

import (
	"log/slog"
	"os"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
)

// version and commit are set at link time via -ldflags
// "-X main.version=... -X main.commit=...", auto-injected by
// amarbel-llc/nixpkgs's buildGoApplication overlay from the derivation's
// `version` and `commit` attrs.
//
// madderBin, direnvBin, dodderBin, papiBin, and ghBin are absolute
// /nix/store paths burned in by `lib.mkSpinclass` at link time. Empty
// values mean the integration is dormant: madder store init and dodder
// repo init are skipped, and direnv/papi/gh fall back to PATH lookup.
var (
	version   = "dev"
	commit    = "unknown"
	madderBin = ""
	direnvBin = ""
	dodderBin = ""
	papiBin   = ""
	ghBin     = ""
)

// buildApp constructs the spinclass command.App with global flags and all
// registered subcommands. It is called from main and from the serve
// subcommand to share a single source of truth for both CLI and MCP
// surfaces.
func buildApp() *command.App {
	app := command.NewApp("spinclass", "Shell-agnostic git worktree session manager")
	app.Version = version + "+" + commit
	app.Aliases = []string{"sc"}
	app.Description.Long = "Manages git worktree session lifecycles: create, attach via configurable session entrypoints, rebase/merge back to main, and clean up. Aliased as `sc`."
	app.PluginAuthor = "amarbel-llc"
	app.PluginDescription = "Git worktree session manager with sweatfile-driven configuration"
	app.MCPArgs = []string{"serve"}
	// Hand-written section 5/7 pages are NOT embedded here: they live as
	// doc/*.N.scd and the flake compiles them with scdoc (spinclass#313).

	app.Params = []command.Param{
		{
			Name:        "format",
			Type:        command.String,
			Description: "Output format: tap or table (default: tap); list also accepts json; merge/check instead take auto|viewport|plain|ndjson (default auto: live viewport on a TTY, ndjson when piped)",
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
	registerVersionCommand(app)
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

// debugLogger returns a *slog.Logger that writes structured records to
// stderr at Debug level when verbose is true, or nil otherwise. Callees
// treat nil as silent. Stderr is intentional: tab-completion captures
// stdout via $(...) / fish (...) substitution, so stderr never enters the
// candidate list and porcelain output on stdout stays clean.
func (g globalArgs) debugLogger() *slog.Logger {
	if !g.Verbose {
		return nil
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}
