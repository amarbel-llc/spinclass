package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/servelog"
)

// Stopping a session's agent and leaving it stopped
// (docs/plans/2026-11-20-stop-start-rights.md). A stop rides the restart
// machinery (FDR 0037): it is a RestartRequest of kind stop, queued through the
// same trusted hand-off, re-checked and consumed by the same preflight, which
// answers the verdict "stop" so the frontend stops the provider and does not
// relaunch it. The stopped mark is spinclass state, in the session record.

const (
	defaultSelfStopReason  = "requested by the session itself"
	defaultChildStopReason = "requested by a holder"
)

// normalizeStopWhen maps a caller's when to the recorded one: "" and idle are
// idle, now is now, anything else is an error.
func normalizeStopWhen(when string) (string, error) {
	switch when {
	case "", restartWhenIdle:
		return restartWhenIdle, nil
	case restartWhenNow:
		return restartWhenNow, nil
	}
	return "", fmt.Errorf("when %q is not supported; use idle or now", when)
}

// queueStop records a stop request on st and runs the restart hand-off, which
// pokes the frontend exactly as for a restart; only the preflight's verdict
// differs. requester is the principal that asked; principal is st's own. A
// failed hand-off clears the request again.
func queueStop(st *session.State, requester, principal, reason, when string) (string, error) {
	return queueStopAs(st, requester, principal, reason, when, false)
}

// queueStopAs is queueStop for a caller known to be confined or not, decided by
// the handler from evidence. A confined caller may not replace a restart or stop
// another party queued (A-M1), and its reason is cleaned and marked (A-M2): see
// queueRestartPostureAs.
func queueStopAs(st *session.State, requester, principal, reason, when string, confinedCaller bool) (string, error) {
	if confinedCaller {
		reason = cleanConfinedReason(reason)
	}
	if st.StoppedAt != nil {
		return "", fmt.Errorf("%s is already stopped (since %s, by %s); start it before stopping it again", st.Key(), st.StoppedAt.Format(time.RFC3339), st.StoppedBy)
	}
	argv, obstacle, err := restartPlan(st, principal, when == restartWhenNow)
	if err != nil {
		return "", err
	}
	if obstacle != "" {
		return "", errors.New(obstacle)
	}
	request := &session.RestartRequest{
		RequestedAt: time.Now().UTC(),
		Requester:   requester,
		Reason:      reason,
		When:        when,
		Kind:        session.RequestKindStop,
	}
	var displaced *session.RestartRequest
	if err := updateLoadedSession(st, func(cur *session.State) bool {
		if confinedCaller && cur.RestartRequest != nil && cur.RestartRequest.Requester != principal {
			req := *cur.RestartRequest
			displaced = &req
			return false
		}
		cur.RestartRequest = request
		return true
	}); err != nil {
		return "", fmt.Errorf("recording the stop request: %w", err)
	}
	if displaced != nil {
		return "", pendingRequestRefusal("stop-this-session", *displaced)
	}
	expanded := expandRestartHandoff(argv, principal, st.Key(), when, "", reason)
	if out, herr := restartHandoffFn(st.WorktreePath, expanded); herr != nil {
		if werr := updateLoadedSession(st, func(cur *session.State) bool {
			cur.RestartRequest = nil
			return true
		}); werr != nil {
			servelog.Errorf("stop clear-request-failed key=%s err=%v", st.Key(), werr)
		}
		return "", fmt.Errorf("the hand-off %q failed (%v), so no stop was queued: %s", expanded[0], herr, out)
	}
	if when == restartWhenNow {
		return fmt.Sprintf(
			"stop NOW queued for %s (%s). The frontend stops the session without waiting for its turn to end, killing that turn and any running merge/check job, and does not relaunch it; it stays stopped until a holder with the start right starts it.",
			st.Key(), reason), nil
	}
	return fmt.Sprintf(
		"stop queued for %s (%s). The frontend stops the session when it next ends a turn and the checks still pass, and does not relaunch it; it stays stopped until a holder with the start right starts it. If you are stopping yourself, end your turn now.",
		st.Key(), reason), nil
}

// approveStop consumes a stop request the preflight approved: under the
// session lock (a close that began since wins), it stamps the in-flight mark a
// re-arm reads and the stopped mark, tells the holders, and answers the stop
// verdict. No relaunch is expected, so no watchdog is armed.
func approveStop(st *session.State, principal string, req session.RestartRequest) (restartVerdict, error) {
	err := session.Update(st.RepoPath, st.Branch, func(cur *session.State) error {
		if cur.ClosingAt != nil {
			return errRestartLostToClose
		}
		now := time.Now().UTC()
		cur.RestartRequest = nil
		cur.RestartingAt = &now
		first := now
		cur.RestartingFirstAt, cur.RestartRearms = &first, 0
		cur.RestartingBy, cur.RestartingReason = req.Requester, req.Reason
		cur.RestartingPosture = ""
		cur.RestartingKind = session.RequestKindStop
		stopped := now
		cur.StoppedAt, cur.StoppedBy, cur.StoppedReason = &stopped, req.Requester, req.Reason
		return nil
	})
	if errors.Is(err, errRestartLostToClose) {
		return noRestart(st.Key() + " is being closed"), nil
	}
	if err != nil {
		return restartVerdict{}, fmt.Errorf("consuming the stop request: %w", err)
	}
	msg := fmt.Sprintf("%s is stopping now and will stay stopped (%s, requested by %s)", st.Key(), req.Reason, req.Requester)
	wakeRestartAudience(st, "restart-preflight", st.RestartWakeTargets(req.Requester, principal), msg)
	return restartVerdict{
		Verdict: verdictStop,
		Reason:  fmt.Sprintf("stop of %s requested by %s: %s", st.Key(), req.Requester, req.Reason),
		When:    req.When,
	}, nil
}

type stopThisSessionParams struct {
	Reason string `json:"reason"`
	When   string `json:"when"`
}

// runStopThisSession is the stop-this-session tool body: a session may stop
// itself with no right, like restart-this-session.
func runStopThisSession(p stopThisSessionParams) (string, error) {
	return runStopThisSessionAs(p, false)
}

// runStopThisSessionAs is runStopThisSession for a caller the handler classified
// (the terminal's own form passes false).
func runStopThisSessionAs(p stopThisSessionParams, confinedCaller bool) (string, error) {
	principal, st, err := callerSession()
	if err != nil {
		return "", err
	}
	when, err := normalizeStopWhen(p.When)
	if err != nil {
		return "", err
	}
	reason := p.Reason
	if reason == "" {
		reason = defaultSelfStopReason
	}
	return queueStopAs(st, principal, principal, reason, when, confinedCaller)
}

func handleStopThisSession(ctx context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var p stopThisSessionParams
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}
	text, err := runStopThisSessionAs(p, confinedCall(ctx))
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

type stopChildParams struct {
	Child  string `json:"child"`
	Reason string `json:"reason"`
	When   string `json:"when"`
}

// runStopChild is the stop-child-session tool body: the handle must carry the
// stop right, and a stop now also the stop-now right.
func runStopChild(p stopChildParams) (string, error) {
	if p.Child == "" {
		return "", errors.New("child is required")
	}
	when, err := normalizeStopWhen(p.When)
	if err != nil {
		return "", err
	}
	const tool = "stop-child-session"
	child, callerPrincipal, err := authorizeChildRight(tool, p.Child, "stop")
	if err != nil {
		return "", err
	}
	if when == restartWhenNow {
		if err := requireHandleRight(tool, "stop-now", callerPrincipal, *child); err != nil {
			return "", err
		}
	}
	childPrincipal, err := ownerPrincipal(child, "", "stop",
		"The operator can stop it from a terminal outside the session.")
	if err != nil {
		return "", err
	}
	reason := p.Reason
	if reason == "" {
		reason = defaultChildStopReason
	}
	return queueStop(child, callerPrincipal, childPrincipal, reason, when)
}

// handleStopChildSession is the `stop-child-session` MCP tool handler. Its
// authority is a handle read from agent-writable session state, so a confined
// caller is refused before anything else.
func handleStopChildSession(ctx context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	if refusal := refuseConfinedServeCaller(ctx, "stop-child-session", "sc stop-child-session"); refusal != nil {
		return refusal, nil
	}
	var p stopChildParams
	if err := json.Unmarshal(args, &p); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	text, err := runStopChild(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

func stopWhenParam() command.Param {
	return command.Param{
		Name:        "when",
		Type:        command.String,
		Description: "idle (default): stop when the session next ends a turn and no merge/check job runs. now: stop at once, killing the current turn and any running job; for a child this needs the stop-now right, and it asks the human unless an operator listed the caller in [[permissions.stop-now-allow]].",
	}
}

func stopThisSessionParamList() []command.Param {
	return []command.Param{
		{
			Name:        "reason",
			Type:        command.String,
			Description: "Why the stop is wanted; shown to the holders. Defaults to \"" + defaultSelfStopReason + "\".",
		},
		stopWhenParam(),
	}
}

func stopChildSessionParamList() []command.Param {
	return []command.Param{
		{
			Name:        "child",
			Type:        command.String,
			Required:    true,
			Description: "The session to stop, as its <repo>/<branch> session key or its worktree directory name — exactly the strings `sc list` prints.",
			Completer:   completeWorktreeTargets,
		},
		{
			Name:        "reason",
			Type:        command.String,
			Description: "Why the stop is wanted; shown to the holders. Defaults to \"" + defaultChildStopReason + "\".",
		},
		stopWhenParam(),
	}
}
