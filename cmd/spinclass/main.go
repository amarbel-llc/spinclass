package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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

// generateArtifacts mirrors command.App.GenerateAllWithSkills minus the
// plugin manifest step. Spinclass owns its .claude-plugin/plugin.json
// (and its sibling clown.json + system-prompt-append.d fragments) directly
// so the manifest can carry a real version. The Nix build copies those
// files into share/purse-first/spinclass/ in postInstall and substitutes
// @VERSION@ at install time.
func generateArtifacts(app *command.App, outDir string) error {
	purseDir := filepath.Join(outDir, "share", "purse-first")
	if err := app.GenerateMappings(purseDir); err != nil {
		return err
	}
	if err := app.GenerateHooks(purseDir); err != nil {
		return err
	}
	if err := app.GenerateManpages(outDir); err != nil {
		return err
	}
	if err := installExtraManpages(app, outDir); err != nil {
		return err
	}
	return app.GenerateCompletions(outDir)
}

// installExtraManpages reproduces the unexported InstallExtraManpages
// from libs/go-mcp/command v0.0.8 so we can compose Generate* steps
// without bumping the dependency.
func installExtraManpages(app *command.App, dir string) error {
	for i, mf := range app.ExtraManpages {
		if mf.Source == nil {
			return fmt.Errorf("ExtraManpages[%d]: Source is nil", i)
		}
		if mf.Path == "" {
			return fmt.Errorf("ExtraManpages[%d]: Path is empty", i)
		}
		if mf.Section <= 0 {
			return fmt.Errorf("ExtraManpages[%d]: Section must be > 0", i)
		}
		if mf.Name == "" {
			return fmt.Errorf("ExtraManpages[%d]: Name is empty", i)
		}

		data, err := fs.ReadFile(mf.Source, mf.Path)
		if err != nil {
			return fmt.Errorf("ExtraManpages[%d]: reading %s: %w", i, mf.Path, err)
		}

		manDir := filepath.Join(dir, "share", "man", fmt.Sprintf("man%d", mf.Section))
		if err := os.MkdirAll(manDir, 0o755); err != nil {
			return fmt.Errorf("ExtraManpages[%d]: creating %s: %w", i, manDir, err)
		}

		dst := filepath.Join(manDir, mf.Name)
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("ExtraManpages[%d]: writing %s: %w", i, dst, err)
		}
	}
	return nil
}

func registerGenerateArtifactsCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:            "generate-artifacts",
		Hidden:          true,
		PassthroughArgs: true,
		Description: command.Description{
			Short: "Generate manpages, mappings, hooks, and shell completions (manifest is owned by Nix)",
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
			return generateArtifacts(app, outDir)
		},
	})
}
