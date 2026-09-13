package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/hooks"
)

const exitImplicitSessionRefused = 3

func implicitSessionKeyCommand() *command.Command {
	return &command.Command{
		Name: "implicit-session-key",
		Description: command.Description{
			Short: "Print the implicit session key a main-checkout agent would get",
			Long: "Print the implicit (main-checkout) session key that spinclass's SessionStart hook WOULD materialize for the given directory and Claude session id, without materializing it (FDR 0014). " +
				"This is a pure plumbing query and a stable contract for launchers (clown#236): it never writes session state, never sweeps orphans, and never writes .spinclass/env. " +
				"Key: <repo>/<rand>, where <repo> is the basename of `git rev-parse --show-toplevel` and <rand> is the lowercase hex of the first 8 bytes (16 hex characters) of sha256(<claude-session-id>). " +
				"Gates, evaluated in order (the same code path the hook uses): the directory and session id are non-empty; the directory is not a git worktree; the directory is its repository's toplevel after symlink resolution (a subdirectory refuses); HEAD is on a branch, any branch (a detached HEAD refuses); and the merged sweatfile hierarchy does not set [hooks].disable-implicit-sessions (an unreadable hierarchy counts as not disabled). " +
				"--cwd defaults to the current directory; a relative --cwd is made absolute. " +
				"Exit status: 0 prints exactly the key and a newline on stdout; 3 means a gate refused, stdout is empty, and one informational line `implicit-session-key: refused: <gate>` goes to stderr (gate names are not part of the contract); any other non-zero status is a usage or internal error. " +
				"CLI only; global --format is ignored.",
		},
		Params: []command.Param{
			{Name: "claude-session-id", Type: command.String, Description: "The Claude Code session id (the hook payload's session_id / claude --session-id)", Required: true},
			{Name: "cwd", Type: command.String, Description: "Directory to evaluate (defaults to the current directory)"},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				ClaudeSessionID string `json:"claude-session-id"`
				Cwd             string `json:"cwd"`
			}
			_ = json.Unmarshal(args, &p)
			cwd := p.Cwd
			if cwd == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				cwd = wd
			}
			abs, err := filepath.Abs(cwd)
			if err != nil {
				return err
			}
			if code := runImplicitSessionKey(os.Stdout, os.Stderr, abs, p.ClaudeSessionID); code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
}

func runImplicitSessionKey(stdout, stderr io.Writer, cwd, sessionID string) int {
	key, refusal := hooks.ImplicitSessionKey(cwd, sessionID)
	if refusal != "" {
		fmt.Fprintf(stderr, "implicit-session-key: refused: %s\n", refusal) //nolint:errcheck // informational only; the exit status carries the refusal
		return exitImplicitSessionRefused
	}
	// A failed write must not exit 0: a launcher would read a truncated key.
	if _, err := fmt.Fprintln(stdout, key); err != nil {
		return 1
	}
	return 0
}
