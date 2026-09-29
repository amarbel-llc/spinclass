package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	spinclose "code.linenisgreat.com/spinclass/internal/close"
	"code.linenisgreat.com/spinclass/internal/session"
)

// closeChildParams is the parameter set of the `close-child-session` tool
// (#249): which spawned child to reap, and whether to override the safety
// refusal close.RunResolved raises for a child holding unmerged work.
type closeChildParams struct {
	Child string `json:"child"`
	Force bool   `json:"force"`
}

// authorizeChildReap decides whether the caller may reap child (#249). The
// authority is a HANDLE (FDR 0032 D12/D13): child.IsHolder(callerPrincipal) —
// the spawner from the moment of spawn (SpawnedByPrincipal), plus anyone later
// granted and accepting a handle (Holders). SpawnedBy/SpawnedByPrincipal are
// otherwise display/legacy: SpawnedBy is the `sc list` lineage column, and a
// child spawned before this change recorded only that session key, so a
// legacy migration path (case 3 below) still lets its original spawner reap
// it by session key.
//
// A session may reap ONLY a child it holds a handle on, so every uncertain
// case is a refusal: an unresolvable caller principal, a child with no
// lineage at all, or a child held by someone else. Failing open here would let
// any session close any other session in `sc list`, which is exactly the
// authority `sc close` deliberately reserves to the session itself and to the
// human.
func authorizeChildReap(callerPrincipal, callerKey string, child session.State) error {
	if callerPrincipal == "" {
		return fmt.Errorf(
			"could not resolve this session's principal, so ownership of %s cannot be established; "+
				"close-child-session only reaps sessions this one holds a handle on",
			child.Key(),
		)
	}
	if child.IsHolder(callerPrincipal) {
		return nil
	}
	// Legacy migration: a child spawned before FDR 0032 carries no
	// SpawnedByPrincipal/Holders at all, only the driver's session key. Its
	// original spawner — identified by that session key — stays able to reap
	// it, so nothing spawned before this change becomes unreapable.
	if child.SpawnedByPrincipal == "" && child.SpawnedBy != "" && callerKey != "" && child.SpawnedBy == callerKey {
		return nil
	}
	if child.SpawnedBy == "" && child.SpawnedByPrincipal == "" && len(child.Holders) == 0 {
		if callerKey != "" {
			return fmt.Errorf(
				"session %s carries no spawned_by lineage — it was not spawned by this session "+
					"(principal %s, session key %s) or any other; a session may only reap sessions it "+
					"holds a handle on. Close it from inside it, or run `sc close %s`",
				child.Key(), callerPrincipal, callerKey, child.Key(),
			)
		}
		return fmt.Errorf(
			"session %s carries no spawned_by lineage — it was not spawned by this session "+
				"(principal %s) or any other; a session may only reap sessions it holds a handle on. "+
				"Close it from inside it, or run `sc close %s`",
			child.Key(), callerPrincipal, child.Key(),
		)
	}
	spawner := child.SpawnedByPrincipal
	if spawner == "" {
		spawner = child.SpawnedBy
	}
	return fmt.Errorf(
		"session %s was spawned by %s (spawned_by=%q, spawned_by_principal=%q), not by this session "+
			"(principal %s, session key %s); a session may only reap sessions it holds a handle on. "+
			"Ask %s to grant a handle (grant-session-handle), or run `sc close %s`",
		child.Key(), spawner, child.SpawnedBy, child.SpawnedByPrincipal, callerPrincipal, callerKey, spawner, child.Key(),
	)
}

// runCloseChild is the shared close-child-session flow: resolve the caller's
// own identity, resolve the named child, check the handle, and hand the reap
// to internal/close.
//
// Every safety check stays with close.RunResolved — it computes the child's
// unintegrated/dirty state itself and, finding no TTY to confirm on, refuses
// with a `--force` hint rather than blocking an MCP caller on a prompt. Force
// is therefore passed straight through and never pre-empted here.
func runCloseChild(p closeChildParams) (string, error) {
	if p.Child == "" {
		return "", errors.New("child is required")
	}

	// callerPrincipal always resolves (FDR 0032 D1); callerKey is best-effort
	// display-only, empty for a caller outside any worktree.
	callerPrincipal := currentPrincipal()
	callerKey := bestEffortSessionKey()

	child, err := session.FindByTarget(p.Child)
	if errors.Is(err, session.ErrTargetNotFound) {
		return "", fmt.Errorf(
			"no spinclass session for child %q; pass the <repo>/<branch> session key or worktree name `sc list` prints",
			p.Child,
		)
	}
	if err != nil {
		// Ambiguity (or index read failure): the error already carries the
		// disambiguating session keys.
		return "", err
	}

	if err := authorizeChildReap(callerPrincipal, callerKey, *child); err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := spinclose.RunResolved(
		&buf, child.RepoPath, child.WorktreePath, child.Branch, p.Force, nil, "tap",
	); err != nil {
		return "", err
	}

	text := fmt.Sprintf("closed child session %s (worktree %s)", child.Key(), child.WorktreePath)
	if tapOut := strings.TrimSpace(buf.String()); tapOut != "" {
		text += "\n" + tapOut
	}
	return text, nil
}

// handleCloseChildSession is the `close-child-session` MCP tool handler.
func handleCloseChildSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var p closeChildParams
	if err := json.Unmarshal(args, &p); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	text, err := runCloseChild(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

// closeChildSessionParamList declares the `close-child-session` parameters.
func closeChildSessionParamList() []command.Param {
	return []command.Param{
		{
			Name:        "child",
			Type:        command.String,
			Required:    true,
			Description: "The child to reap, as its <repo>/<branch> session key or its worktree directory name — exactly the strings `sc list` prints.",
			Completer:   completeWorktreeTargets,
		},
		{
			Name:        "force",
			Type:        command.Bool,
			Description: "Reap even when the child has uncommitted changes or commits not yet integrated into the default branch. Without it such a child is refused so its work is not silently discarded; a clean child with nothing to lose needs no force. Setting it always prompts the human — no allow-list can approve it silently — so reach for it only when the child's work is genuinely disposable.",
		},
	}
}
