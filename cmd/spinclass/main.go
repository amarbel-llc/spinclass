package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	commandhuh "github.com/amarbel-llc/purse-first/libs/go-mcp/command/huh"
)

func main() {
	app := buildApp()

	ctx := context.Background()
	if err := app.RunCLI(ctx, os.Args[1:], commandhuh.Prompter{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func registerGenerateArtifactsCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:            "generate-artifacts",
		Hidden:          true,
		PassthroughArgs: true,
		Description: command.Description{
			Short: "Generate plugin manifest, manpages, and shell completions",
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				Args []string `json:"args"`
			}
			_ = json.Unmarshal(args, &p)
			outDir := "."
			if len(p.Args) > 0 {
				outDir = p.Args[0]
			}
			return app.GenerateAll(outDir)
		},
	})
}
