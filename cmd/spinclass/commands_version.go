package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/protocol"
)

func registerVersionCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "version",
		Title: "Print Spinclass Version",
		Description: command.Description{
			Short: "Print spinclass version and any build-time-pinned tools",
			Long:  "Print a table of components: spinclass itself plus any binaries pinned via lib.mkSpinclass (madder, direnv). Empty pins display as `dormant`. Version and commit are injected at build time as `<version>+<commit>`; a devshell `go build` reports `dev+unknown`.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Run: func(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
			return command.TextResult(formatVersionTable(version, commit, madderBin, direnvBin)), nil
		},
	})
}

func formatVersionTable(spinclassVersion, spinclassCommit, madder, direnv string) string {
	var sb strings.Builder
	tw := tabwriter.NewWriter(&sb, 0, 2, 2, ' ', 0)

	fmt.Fprintln(tw, "COMPONENT\tVERSION\tREV")

	selfComp := "spinclass-" + spinclassVersion + "/spinclass"
	selfVer := spinclassVersion + "+" + spinclassCommit
	fmt.Fprintf(tw, "%s\t%s\t%s\n", selfComp, selfVer, spinclassCommit)

	writePinRow(tw, "madder", madder)
	writePinRow(tw, "direnv", direnv)

	tw.Flush()
	return sb.String()
}

func writePinRow(tw *tabwriter.Writer, name, binPath string) {
	if binPath == "" {
		fmt.Fprintf(tw, "%s\t-\tdormant\n", name)
		return
	}
	comp, ver, rev := parseStorePathBinary(binPath)
	if comp == "" {
		fmt.Fprintf(tw, "%s\t?\t%s\n", name, binPath)
		return
	}
	fmt.Fprintf(tw, "%s\t%s\t%s\n", comp, ver, rev)
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
