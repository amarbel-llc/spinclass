package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/spinclass/internal/spawn"
)

func registerVersionCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "version",
		Title: "Print Spinclass Version",
		Description: command.Description{
			Short: "Print spinclass version and any build-time-pinned tools",
			Long:  "Print a table of components: spinclass itself plus any binaries pinned via lib.mkSpinclass (madder, direnv, papi, gh). Empty pins display as `dormant`. Version and commit are injected at build time as `<version>+<commit>`; a devshell `go build` reports `dev+unknown`. Called as the MCP tool (in a long-lived serve), a last line says whether the serve still runs the spinclass on PATH or is stale after a deploy (spinclass#399).",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Run: func(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
			return command.TextResult(formatVersionTable(version, commit, madderBin, direnvBin, papiBin, ghBin) + serveBinaryLine()), nil
		},
	})
}

func formatVersionTable(spinclassVersion, spinclassCommit, madder, direnv, papi, gh string) string {
	var sb strings.Builder
	tw := tabwriter.NewWriter(&sb, 0, 2, 2, ' ', 0)

	// Writes target a tabwriter over an in-memory strings.Builder, so they
	// cannot fail; discard the errors explicitly to satisfy errcheck.
	_, _ = fmt.Fprintln(tw, "COMPONENT\tVERSION\tREV")

	selfComp := "spinclass-" + spinclassVersion + "/spinclass"
	selfVer := spinclassVersion + "+" + spinclassCommit
	_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", selfComp, selfVer, spinclassCommit)

	writePinRow(tw, "madder", madder)
	writePinRow(tw, "direnv", direnv)
	writePinRow(tw, "papi", papi)
	writePinRow(tw, "gh", gh)

	_ = tw.Flush()
	return sb.String()
}

// inServe is set when this process runs `spinclass serve`.
var inServe bool

// serveBinaryLine says, in a serve only, whether this process still runs the
// spinclass on PATH (spinclass#399): a long-lived serve's table is the binary
// it started with, which a deploy does not replace. The CLI table is unchanged.
func serveBinaryLine() string {
	if !inServe {
		return ""
	}
	b, err := spawn.CurrentServeBinary()
	return formatServeBinaryLine(b, err)
}

// formatServeBinaryLine renders the freshness verdict. Two paths that are one
// known build (the clown plugin builds spinclass as its own derivation) are
// not stale.
func formatServeBinaryLine(b spawn.ServeBinary, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("serve binary: unknown (%v)\n", err)
	case b.SameBuildDifferentPath():
		return fmt.Sprintf("serve binary: same build, different path: running %s, spinclass on PATH is %s (both %s)\n", b.Running, b.OnPath, b.RunningBuild)
	case b.IdentityErr != nil:
		return fmt.Sprintf("serve binary: STALE: running %s (%s), spinclass on PATH is %s; cannot read the build identity of %s: %v; restart the session (spinclass#399)\n",
			b.Running, b.RunningBuild, b.OnPath, b.OnPath, b.IdentityErr)
	case b.Stale():
		return fmt.Sprintf("serve binary: STALE: running %s (%s), spinclass on PATH is %s (%s); restart the session (spinclass#399)\n",
			b.Running, b.RunningBuild, b.OnPath, b.OnPathBuild)
	default:
		return "serve binary: current (" + b.Running + ")\n"
	}
}

func writePinRow(tw *tabwriter.Writer, name, binPath string) {
	// Infallible writes to a tabwriter over an in-memory builder (see RunVersion).
	if binPath == "" {
		_, _ = fmt.Fprintf(tw, "%s\t-\tdormant\n", name)
		return
	}
	comp, ver, rev := parseStorePathBinary(binPath)
	if comp == "" {
		_, _ = fmt.Fprintf(tw, "%s\t?\t%s\n", name, binPath)
		return
	}
	_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", comp, ver, rev)
}

// pnameVersionRe splits a `<pname>-<version>` segment of a /nix/store
// path. Nix versions conventionally start with a digit (or `v` + digit),
// which lets us locate the boundary even when pname itself contains
// dashes (`claude-code-2.1.111` → `claude-code` / `2.1.111`).
var pnameVersionRe = regexp.MustCompile(`^(.+?)-(v?\d.*)$`)

// parseStorePathBinary turns `/nix/store/<hash>-<pname>-<ver>/bin/<bin>`
// into a clown-style triple: COMPONENT (`<pname>-<ver>/<bin>`),
// VERSION, and REV (the 32-char store hash). Returns empty component
// when the path doesn't match the expected shape; callers fall back
// to printing the raw path.
func parseStorePathBinary(binPath string) (component, version, rev string) {
	binary := filepath.Base(binPath)
	derivDir := filepath.Dir(filepath.Dir(binPath))
	derivBase := filepath.Base(derivDir)

	if len(derivBase) <= 33 || derivBase[32] != '-' {
		return "", "", ""
	}
	rev = derivBase[:32]
	pnameVersion := derivBase[33:]

	matches := pnameVersionRe.FindStringSubmatch(pnameVersion)
	if matches == nil {
		return "", "", ""
	}
	pname := matches[1]
	version = matches[2]
	component = pname + "-" + version + "/" + binary
	return component, version, rev
}
