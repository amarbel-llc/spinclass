package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/session"
)

// noHandleMessage renders the FDR 0032 D12/D13 refusal wording shared by
// every handle-tool flow that requires the caller to already hold a handle on
// child — the same "no handle to X (spawned by P; ask P to grant …)" style
// authorizeChildReap uses (#249), so every refusal in this family reads the
// same regardless of which tool raised it.
func noHandleMessage(callerPrincipal string, child session.State) string {
	spawner := child.SpawnedByPrincipal
	if spawner == "" {
		spawner = child.SpawnedBy
	}
	if spawner == "" {
		return fmt.Sprintf(
			"no handle to %s, and it carries no spawned_by lineage either — this session (principal %s) holds no handle on it",
			child.Key(), callerPrincipal,
		)
	}
	return fmt.Sprintf(
		"no handle to %s (spawned by %s; ask %s to grant a handle via grant-session-handle)",
		child.Key(), spawner, spawner,
	)
}

// findHandleTarget resolves a handle-tool's `child` parameter, translating
// session.ErrTargetNotFound into the same wording close-child-session uses so
// the three tools' misses read identically.
func findHandleTarget(target string) (*session.State, error) {
	child, err := session.FindByTarget(target)
	if errors.Is(err, session.ErrTargetNotFound) {
		return nil, fmt.Errorf(
			"no spinclass session for child %q; pass the <repo>/<branch> session key or worktree name `sc list` prints",
			target,
		)
	}
	return child, err
}

// grantHandleParams is the parameter set of the `grant-session-handle` tool
// (FDR 0032 D12).
type grantHandleParams struct {
	Child  string `json:"child"`
	To     string `json:"to"`
	Rights string `json:"rights"`
}

// runGrantSessionHandle is the shared grant flow (FDR 0032 D12: grant is a
// message). The granter must itself hold a handle on child — a pending
// holder exercising this tool accepts first (granting onward is itself the
// first use, same as reaping). spinclass never sends the recipient a chat
// message on its own behalf (chat is clown/troupe's, not spinclass's); the
// result text tells the caller to relay the child's key instead.
func runGrantSessionHandle(p grantHandleParams) (string, error) {
	if p.Child == "" {
		return "", errors.New("child is required")
	}
	if p.To == "" {
		return "", errors.New("to is required")
	}

	callerPrincipal := currentPrincipal()

	child, err := findHandleTarget(p.Child)
	if err != nil {
		return "", err
	}

	if child.IsPendingHolder(callerPrincipal) {
		child.AcceptHandle(callerPrincipal)
	}
	if !child.IsHolder(callerPrincipal) {
		return "", errors.New(noHandleMessage(callerPrincipal, *child))
	}

	child.GrantHandle(p.To, p.Rights)
	if err := session.Write(*child); err != nil {
		return "", fmt.Errorf("writing session state: %w", err)
	}

	return fmt.Sprintf(
		"granted handle on %s to %s (pending until first use; rights: %s). Send %s the child's session key (%s) via chat so it can accept.",
		child.Key(), p.To, child.HandleRights[p.To], p.To, child.Key(),
	), nil
}

// handleGrantSessionHandle is the `grant-session-handle` MCP tool handler.
func handleGrantSessionHandle(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var p grantHandleParams
	if err := json.Unmarshal(args, &p); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	text, err := runGrantSessionHandle(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

// grantSessionHandleParamList declares the `grant-session-handle` parameters.
func grantSessionHandleParamList() []command.Param {
	return []command.Param{
		{
			Name:        "child",
			Type:        command.String,
			Required:    true,
			Description: "The session to grant a handle on, as its <repo>/<branch> session key or its worktree directory name — exactly the strings `sc list` prints.",
			Completer:   completeWorktreeTargets,
		},
		{
			Name:        "to",
			Type:        command.String,
			Required:    true,
			Description: "The recipient PRINCIPAL — the recipient session's per-instance key (its chat JID localpart, as `chat_list` shows it), not a session key.",
		},
		{
			Name:        "rights",
			Type:        command.String,
			Description: "Comma-separated object rights to record (FDR 0032 D13: observe, close, grant, instruct, cap, merge, check). Recorded but NOT enforced in this slice — slice 1 replaces this view with signed grant-record digests. Defaults to \"observe,close\" when omitted, the conservative lazy handoff.",
		},
	}
}

// releaseHandleParams is the parameter set of the `release-session-handle`
// tool.
type releaseHandleParams struct {
	Child string `json:"child"`
}

// runReleaseSessionHandle is the shared release flow (FDR 0032 D12: release
// is local — the caller removes only itself). Refuses legibly, changing
// nothing, when the caller held no handle on child at all.
func runReleaseSessionHandle(p releaseHandleParams) (string, error) {
	if p.Child == "" {
		return "", errors.New("child is required")
	}

	callerPrincipal := currentPrincipal()

	child, err := findHandleTarget(p.Child)
	if err != nil {
		return "", err
	}

	if !child.ReleaseHandle(callerPrincipal) {
		return "", fmt.Errorf(
			"this session (principal %s) holds no handle on %s — nothing to release",
			callerPrincipal, child.Key(),
		)
	}
	if err := session.Write(*child); err != nil {
		return "", fmt.Errorf("writing session state: %w", err)
	}

	if len(child.Holders) == 0 {
		return fmt.Sprintf("released handle on %s (orphan: no holders remain)", child.Key()), nil
	}
	return fmt.Sprintf(
		"released handle on %s (remaining holders: %s)", child.Key(), strings.Join(child.Holders, ", "),
	), nil
}

// handleReleaseSessionHandle is the `release-session-handle` MCP tool
// handler.
func handleReleaseSessionHandle(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var p releaseHandleParams
	if err := json.Unmarshal(args, &p); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	text, err := runReleaseSessionHandle(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

// releaseSessionHandleParamList declares the `release-session-handle`
// parameters.
func releaseSessionHandleParamList() []command.Param {
	return []command.Param{
		{
			Name:        "child",
			Type:        command.String,
			Required:    true,
			Description: "The session to release this session's handle on, as its <repo>/<branch> session key or its worktree directory name — exactly the strings `sc list` prints.",
			Completer:   completeWorktreeTargets,
		},
	}
}

// listHandlesParams is the parameter set of the `list-handles` tool.
type listHandlesParams struct {
	Accept bool `json:"accept"`
}

// displayRights renders the recorded rights (FDR 0032 D13) for principal on
// s, or "(implicit)" when principal holds via the spawner's implicit handle
// (SpawnedByPrincipal) with no recorded grant — the spawner's own handle at
// spawn time is never itself a "grant", so it never gets a HandleRights entry.
func displayRights(s *session.State, principal string) string {
	if r, ok := s.HandleRights[principal]; ok {
		return r
	}
	return "(implicit)"
}

// runListHandles is the shared list-handles flow: scan every session
// (worktree and implicit — session.ListAll walks the one central index that
// carries both, see FDR 0014) and report every one where the caller holds or
// is pending a handle. With accept, every pending one is exercised (accepted)
// first, per FDR 0032 D12's first-use rule.
func runListHandles(p listHandlesParams) (string, error) {
	callerPrincipal := currentPrincipal()

	all, err := session.ListAll(nil)
	if err != nil {
		return "", fmt.Errorf("listing sessions: %w", err)
	}

	var lines []string
	for i := range all {
		s := &all[i]
		pending := s.IsPendingHolder(callerPrincipal)
		if pending && p.Accept {
			if s.AcceptHandle(callerPrincipal) {
				if err := session.Write(*s); err != nil {
					return "", fmt.Errorf("accepting handle on %s: %w", s.Key(), err)
				}
				pending = false
			}
		}
		switch {
		case s.IsHolder(callerPrincipal):
			lines = append(lines, fmt.Sprintf("held    %s  rights=%s", s.Key(), displayRights(s, callerPrincipal)))
		case pending:
			lines = append(lines, fmt.Sprintf("pending %s  rights=%s", s.Key(), displayRights(s, callerPrincipal)))
		}
	}
	if len(lines) == 0 {
		return "no handles", nil
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
}

// handleListHandles is the `list-handles` MCP tool handler.
func handleListHandles(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var p listHandlesParams
	if err := json.Unmarshal(args, &p); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	text, err := runListHandles(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

// listHandlesParamList declares the `list-handles` parameters.
func listHandlesParamList() []command.Param {
	return []command.Param{
		{
			Name:        "accept",
			Type:        command.Bool,
			Description: "Accept every pending handle before reporting (FDR 0032 D12: accept is first use) — without it, pending grants are reported but confer no authority yet.",
		},
	}
}

// runWhoami is the shared `sc whoami` / `whoami` flow: this session's
// principal (FDR 0032 D1), its session key when it has one, then the same
// held/pending handle listing as list-handles.
func runWhoami() (string, error) {
	principal := currentPrincipal()
	key := bestEffortSessionKey()
	if key == "" {
		key = "(none)"
	}
	handles, err := runListHandles(listHandlesParams{})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("principal: %s\nsession_key: %s\n%s", principal, key, handles), nil
}

// handleWhoami is the `whoami` MCP tool handler. Registered with Run and no
// RunCLI (the close-child-session/resurrect pattern, commands_mcp_only.go):
// the App's CLI dispatch runs Run directly when RunCLI is absent, so this one
// registration serves both `sc whoami` and the `whoami` MCP tool — the same
// reason those two commands need no separate CLI registration either.
func handleWhoami(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
	text, err := runWhoami()
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}
