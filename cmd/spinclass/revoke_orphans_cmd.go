package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/charlie/auth"
	"code.linenisgreat.com/spinclass/internal/spawn"
)

// Seams for command tests.
var (
	revokeOrphansSweep = auth.SweepOrphans
	revokeOrphansOut   = func() io.Writer { return os.Stdout }
	revokeOrphansHome  = os.UserHomeDir
	revokeOrphansCwd   = os.Getwd
)

// registerRevokeOrphansCommand registers `sc revoke-orphans`. CLI-only (RunCLI,
// no Run): it runs the repo's configured [auth].revoke-command, so it is an
// operator action, not an agent tool.
func registerRevokeOrphansCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "revoke-orphans",
		Title: "Revoke orphaned session credentials",
		Description: command.Description{
			Short: "Revoke the forge credentials of a repo's ended sessions whose close-time revoke failed (FDR 0028)",
			Long: "Runs the same sweep a new session start runs: every closed or abandoned session of the repo whose " +
				"minted credential was never marked revoked has the [auth].revoke-command pinned on its session record at mint run for it. " +
				"A revoke that fails again is reported and left for the next sweep. " +
				"The repo is a dirname leaf-searched under $HOME/*/repos, an explicit path, or, when omitted, the repo of the current directory.",
		},
		Params: []command.Param{
			{Name: "repo", Type: command.String, Description: "Repo dirname or path; default the current directory's repo"},
		},
		RunCLI: runRevokeOrphans,
	})
}

func runRevokeOrphans(ctx context.Context, args json.RawMessage) error {
	var f struct {
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal(args, &f); err != nil {
		return fmt.Errorf("revoke-orphans: %w", err)
	}
	home, err := revokeOrphansHome()
	if err != nil {
		return fmt.Errorf("revoke-orphans: %w", err)
	}
	repoPath, err := resolveRevokeOrphansRepo(home, f.Repo)
	if err != nil {
		return fmt.Errorf("revoke-orphans: %w", err)
	}
	return reportRevokeOrphans(ctx, repoPath, revokeOrphansOut())
}

func resolveRevokeOrphansRepo(home, repo string) (string, error) {
	if repo != "" {
		return spawn.ResolveRepo(home, repo)
	}
	cwd, err := revokeOrphansCwd()
	if err != nil {
		return "", err
	}
	return git.DetectRepo(cwd)
}

func reportRevokeOrphans(ctx context.Context, repoPath string, out io.Writer) error {
	revoked, errs := revokeOrphansSweep(ctx, repoPath, os.Stderr)
	fmt.Fprintf(out, "%s: revoked %d orphaned credential(s)\n", repoPath, revoked)
	for _, e := range errs {
		fmt.Fprintf(out, "  not revoked (left for the next sweep): %v\n", e)
	}
	if len(errs) > 0 {
		return fmt.Errorf("revoke-orphans: %d credential(s) could not be revoked", len(errs))
	}
	return nil
}
