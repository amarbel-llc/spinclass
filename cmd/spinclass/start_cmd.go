package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/servelog"
)

// Starting a stopped session's agent again
// (docs/plans/2026-11-20-stop-start-rights.md). start-child-session runs the
// trusted [session-entry].start-handoff, which relaunches the same principal
// detached with the conversation resumed and one first message: a notice of the
// stop and the start, then the caller's message.

type startChildParams struct {
	Child   string `json:"child"`
	Message string `json:"message"`
}

// startNotice is the one notice the resumed agent reads before the caller's
// message.
func startNotice(st *session.State, starter string, now time.Time, message string) string {
	stoppedAt := "an unrecorded time"
	if st.StoppedAt != nil {
		stoppedAt = st.StoppedAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(
		"You were stopped at %s by %s (%s) and started again at %s by %s. Nothing else changed in your session; carry on from where you were.\n\n%s",
		stoppedAt, st.StoppedBy, st.StoppedReason, now.UTC().Format(time.RFC3339), starter, message)
}

// expandStartHandoff substitutes the start hand-off's placeholders in one pass,
// so a message that itself contains "{principal}" stays literal.
func expandStartHandoff(argv []string, principal, key, message string) []string {
	r := strings.NewReplacer(principalPlaceholder, principal, "{session-key}", key, "{message}", message)
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = r.Replace(a)
	}
	return out
}

// runStartChild is the start-child-session tool body: the handle must carry the
// start right, the child must be stopped, and a first message is required.
func runStartChild(p startChildParams) (string, error) {
	if p.Child == "" {
		return "", errors.New("child is required")
	}
	if strings.TrimSpace(p.Message) == "" {
		return "", errors.New("message is required: a started session reads it as its first message, after a notice of the stop and the start")
	}
	const tool = "start-child-session"
	child, callerPrincipal, err := authorizeChildRight(tool, p.Child, "start")
	if err != nil {
		return "", err
	}
	if child.StoppedAt == nil {
		return "", fmt.Errorf("%s is not stopped, so there is nothing to start (stop it first, or restart it with restart-child-session)", child.Key())
	}
	childPrincipal, err := ownerPrincipal(child, "", "start",
		"The operator can start it from a terminal outside the session.")
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving the home directory to read sweatfiles: %w", err)
	}
	h, err := sweatfileio.LoadHierarchy(home, child.RepoPath)
	if err != nil {
		return "", fmt.Errorf("reading the sweatfile hierarchy: %w", err)
	}
	argv, _ := sweatfileio.TrustedStartHandoff(h, home, child.RepoPath)
	if len(argv) == 0 {
		return "", fmt.Errorf(
			"%s cannot be started: no start hand-off is configured; an operator must set `start-handoff` under [session-entry] in the global sweatfile or one above the repo (a repo or worktree sweatfile is ignored)",
			child.Key())
	}

	now := time.Now().UTC()
	expanded := expandStartHandoff(argv, childPrincipal, child.Key(), startNotice(child, callerPrincipal, now, p.Message))
	if out, herr := restartHandoffFn(child.WorktreePath, expanded); herr != nil {
		return "", fmt.Errorf("the start hand-off %q failed (%v), so %s is still stopped: %s", expanded[0], herr, child.Key(), out)
	}
	stoppedAt := *child.StoppedAt
	if err := updateLoadedSession(child, func(cur *session.State) bool {
		if cur.StoppedAt == nil || !cur.StoppedAt.Equal(stoppedAt) {
			return false
		}
		cur.StoppedAt, cur.StoppedBy, cur.StoppedReason = nil, "", ""
		return true
	}); err != nil {
		servelog.Errorf("start clear-stop-failed key=%s err=%v", child.Key(), err)
		return "", fmt.Errorf("the start hand-off ran, but %s's stopped mark could not be cleared: %w", child.Key(), err)
	}
	return fmt.Sprintf(
		"started %s: its frontend relaunches it detached with its conversation resumed; it reads the notice and your message first. Its holders hear when it is back.",
		child.Key()), nil
}

// handleStartChildSession is the `start-child-session` MCP tool handler. Its
// authority is a handle read from agent-writable session state, so a confined
// caller is refused before anything else.
func handleStartChildSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	if refusal := refuseConfinedServeCaller("start-child-session", "sc start-child-session"); refusal != nil {
		return refusal, nil
	}
	var p startChildParams
	if err := json.Unmarshal(args, &p); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	text, err := runStartChild(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

func startChildSessionParamList() []command.Param {
	return []command.Param{
		{
			Name:        "child",
			Type:        command.String,
			Required:    true,
			Description: "The stopped session to start, as its <repo>/<branch> session key or its worktree directory name — exactly the strings `sc list` prints.",
			Completer:   completeWorktreeTargets,
		},
		{
			Name:        "message",
			Type:        command.String,
			Required:    true,
			Description: "The started session's first message, after a notice of who stopped it, why and when, and who started it. Required.",
		},
	}
}
