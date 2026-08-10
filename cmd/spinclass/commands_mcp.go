package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/purse-first/libs/go-mcp/server"
	"code.linenisgreat.com/purse-first/libs/go-mcp/transport"

	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/embeds"
	"code.linenisgreat.com/spinclass/internal/resources"
	"code.linenisgreat.com/spinclass/internal/servelog"
	"code.linenisgreat.com/spinclass/internal/sysprompt"
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

			// Install the intra-session merge-queue completion hook (spinclass#265,
			// FDR 0025) before any async job can run. Harmless without clown (the
			// async tools that populate the queue are clown-gated), so it is
			// unconditional — a completing check/merge just finds an empty queue.
			wireMergeQueue()

			// ProtocolVersion gate (#26, RFC-0018). Under clown, verify the
			// jobwake this binary linked at build time matches the hosting
			// ringmaster's observable protocol. A mismatch does NOT stop serve:
			// async wakes and cancel-observe are pure `ringmaster` CLI
			// shell-outs, self-consistent with the running binary, so they keep
			// working. It DOES disable the in-process liveness flock
			// (internal/job consults the same memoized CheckProtocol), so warn
			// loudly here and via the system-prompt fragment. Silent on a clean
			// match; only skew (or an old ringmaster with no `--protocol`)
			// surfaces.
			if clown.Enabled() {
				ok, want, got, perr := clown.CheckProtocol(sigCtx)
				clown.SetFlockEnabled(ok)
				if !ok {
					detail := fmt.Sprintf("linked jobwake ProtocolVersion=%d, ringmaster reports %d", want, got)
					if perr != nil {
						detail = fmt.Sprintf("linked jobwake ProtocolVersion=%d, could not read `ringmaster version --protocol`: %v", want, perr)
					}
					servelog.Errorf("serve.protocol mismatch: %s; per-job liveness flock disabled", detail)
					fmt.Fprintf(os.Stderr,
						"spinclass serve: ⚠ ringmaster protocol mismatch: %s; per-job liveness flock disabled (async wakes unaffected)\n",
						detail)
				}
			}

			registry := server.NewToolRegistryV1()
			app.RegisterMCPToolsV1(registry)

			// Resource provider is registered only when madder is build-time
			// pinned: that's the same gate that controls whether merge/check
			// emit `resource_link` URIs in the first place. Capturing cwd
			// here scopes every `resources/read` call to this serve
			// process's worktree.
			var resourceProvider server.ResourceProviderV1
			if madderBin := embeds.MadderBin(); madderBin != "" {
				cwd, cwdErr := os.Getwd()
				if cwdErr != nil {
					return fmt.Errorf("resolving cwd for resource provider: %w", cwdErr)
				}
				resourceProvider = resources.NewMadderProvider(cwd, madderBin)
			}

			// Dynamic system-prompt fragment (clown plugin protocol, RFC-0002
			// §5; spinclass#187). clown's stdio bridge issues a `prompts/get`
			// for sysprompt.PromptName BEFORE `initialize`; go-mcp answers the
			// cold request via the V0 path (a V0-only PromptRegistry can't
			// force V1 negotiation). The renderer is best-effort: it always
			// returns a result, and an empty body simply contributes nothing.
			prompts := server.NewPromptRegistry()
			prompts.Register(
				protocol.Prompt{
					Name:        sysprompt.PromptName,
					Description: "Live orientation for the current spinclass session.",
				},
				func(_ context.Context, _ map[string]string) (*protocol.PromptGetResult, error) {
					text, renderErr := sysprompt.Render(sysprompt.Resolve())
					if renderErr != nil {
						servelog.Errorf("serve.prompt render err=%v", renderErr)
					}
					return &protocol.PromptGetResult{
						Description: "spinclass session orientation",
						Messages: []protocol.PromptMessage{{
							Role:    "user",
							Content: protocol.TextContent(text),
						}},
					}, nil
				},
			)

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
				Resources:     resourceProvider,
				Prompts:       prompts,
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
