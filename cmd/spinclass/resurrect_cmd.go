package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/resurrect"
	"code.linenisgreat.com/spinclass/internal/session"
)

// resurrectParams is the parameter set of the `resurrect` tool (#291): the
// closed session to recreate, and an optional replacement branch name.
type resurrectParams struct {
	Target    string `json:"target"`
	NewBranch string `json:"new-branch"`
}

// runResurrect resolves the shared errors resurrect.Run's callers need
// distinct wording for (target not found vs. ambiguous — the same shapes
// close/resume already use) and hands the rest to internal/resurrect.
func runResurrect(p resurrectParams) (string, error) {
	if p.Target == "" {
		return "", errors.New("target is required")
	}

	var buf bytes.Buffer
	if err := resurrect.Run(&buf, p.Target, p.NewBranch, "tap"); err != nil {
		if errors.Is(err, session.ErrTargetNotFound) {
			return "", fmt.Errorf(
				"no spinclass session for target %q; pass the <repo>/<branch> session key or worktree name `sc list --closed` prints",
				p.Target,
			)
		}
		return "", err
	}

	return buf.String(), nil
}

// handleResurrect is the `resurrect` MCP tool handler.
func handleResurrect(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var p resurrectParams
	if err := json.Unmarshal(args, &p); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	text, err := runResurrect(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

// resurrectParamList declares the `resurrect` parameters.
func resurrectParamList() []command.Param {
	return []command.Param{
		{
			Name:        "target",
			Type:        command.String,
			Required:    true,
			Description: "Closed session to recreate, as its <repo>/<branch> session key or worktree directory name — exactly the strings `sc list --closed` prints.",
			Completer:   completeWorktreeTargets,
		},
		{
			Name:        "new-branch",
			Type:        command.String,
			Description: "Branch name to use instead of the original (must not contain '.') — e.g. if something else already recreated a branch with that name.",
		},
	}
}
