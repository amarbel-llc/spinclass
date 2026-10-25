package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/0/clown"
	"code.linenisgreat.com/spinclass/internal/confinement"
	"code.linenisgreat.com/spinclass/internal/job"
	"code.linenisgreat.com/spinclass/internal/servelog"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

// restart-this-session and restart-child-session (#419, FDR 0037) split a
// restart in two. Spinclass AUTHORIZES, REFUSES and RECORDS it: it checks that
// the operator configured a hand-off, that nothing is running that a restart
// would kill, and (for a child) that the caller holds the restart right; then it
// records a RestartRequest and execs the hand-off. The harness frontend (clown)
// performs the stop and relaunch, at the session's next idle transition, after
// asking `restart-preflight`.
//
// The hand-off is [session-entry].restart in a sweatfile layer above the repo,
// read live and never pinned, so an agent cannot choose the command spinclass
// runs and sessions that already exist get it.
//
// An unforced restart waits for the turn to end and is refused while a job
// runs. `force` is the operator-gated escalation (perms.AlwaysAsk makes the
// tool call prompt the human; the CLI caller is the operator): it records
// when=now and skips the running-job obstacle, at request time and in the
// preflight, so it kills the current turn and any running job. cancel-restart
// removes a queued request.

const (
	restartWhenIdle           = "idle"
	restartWhenNow            = "now"
	restartHandoffTimeout     = 15 * time.Second
	restartNotifyTimeout      = 15 * time.Second
	defaultSelfRestartReason  = "requested by the session itself"
	defaultChildRestartReason = "requested by a holder"
)

// errNoRestartSession is returned when the caller's principal maps to no session.
var errNoRestartSession = errors.New(
	"could not tie this call to a spinclass session, so there is nothing to restart")

// restartHandoffFn is the seam over exec'ing the hand-off, so tests assert the
// directory and the expanded argv without a real command.
var restartHandoffFn = runRestartHandoff

// notifyPrincipalFn is a seam over clown.NotifyPrincipal, the non-exit wake
// helper, so the preflight's holder notification is assertable.
var notifyPrincipalFn = clown.NotifyPrincipal

// runRestartHandoff execs argv directly (never through a shell) in dir with a
// bounded wait, and returns its trimmed combined output.
func runRestartHandoff(dir string, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("the restart hand-off is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), restartHandoffTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s: %w", restartHandoffTimeout, ctx.Err())
	}
	return strings.TrimSpace(string(out)), err
}

// expandRestartHandoff substitutes the hand-off placeholders into every element
// of argv. The replacement is a single pass, so a reason that itself contains
// "{principal}" stays literal.
func expandRestartHandoff(argv []string, principal, key, when, reason string) []string {
	r := strings.NewReplacer(
		principalPlaceholder, principal,
		"{session-key}", key,
		"{when}", when,
		"{reason}", reason,
	)
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = r.Replace(a)
	}
	return out
}

// restartPlan returns the hand-off argv template for st and why st must not be
// restarted right now ("" when it may). It is the checks both tools and the
// preflight share, so a request that passes here is one the preflight also
// accepts unless something changed in between. principal is the session's own
// principal, the one {principal} expands to and clown restarts it by. force
// skips the running-job obstacle and nothing else.
func restartPlan(st *session.State, principal string, force bool) (argv []string, obstacle string, err error) {
	if st.Kind == session.KindImplicit {
		return nil, fmt.Sprintf("%s is a main-checkout (implicit) session; only worktree sessions can be restarted", st.Key()), nil
	}
	if _, _, perr := applyRecordedPrincipal([]string{principalPlaceholder}, principal, false, false); perr != nil {
		return nil, fmt.Sprintf("%s cannot be restarted: %v", st.Key(), perr), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", fmt.Errorf("resolving the home directory to read sweatfiles: %w", err)
	}
	h, err := sweatfileio.LoadHierarchy(home, st.RepoPath)
	if err != nil {
		return nil, "", fmt.Errorf("reading the sweatfile hierarchy: %w", err)
	}
	argv, _ = sweatfileio.TrustedRestartHandoff(h, home, st.RepoPath)
	if len(argv) == 0 {
		return nil, fmt.Sprintf(
			"%s cannot be restarted: no restart hand-off is configured; an operator must set `restart` under [session-entry] in the global sweatfile or one above the repo (a repo or worktree sweatfile is ignored)",
			st.Key()), nil
	}

	// force is the operator-approved escalation (always-ask in perms.AlwaysAsk,
	// FDR 0037): it accepts killing a running job, and only that. Every check
	// above still stands.
	if j, jerr := job.Read(st.WorktreePath); !force && jerr == nil && j.Status == job.StatusRunning {
		return nil, fmt.Sprintf("a %s job (%s) is still running in %s; a restart would kill it, so let it finish or cancel it first (or restart with force, which asks the operator)", j.Kind, j.ID, st.Key()), nil
	}
	return argv, "", nil
}

// queueRestart records a RestartRequest on st and runs the hand-off. requester
// is the principal that asked; principal is st's own. A failed hand-off clears
// the request again, so the frontend never restarts on a request that was not
// handed off.
func queueRestart(st *session.State, requester, principal, reason string, force bool) (string, error) {
	argv, obstacle, err := restartPlan(st, principal, force)
	if err != nil {
		return "", err
	}
	if obstacle != "" {
		return "", errors.New(obstacle)
	}

	when := restartWhenIdle
	if force {
		when = restartWhenNow
	}
	st.RestartRequest = &session.RestartRequest{
		RequestedAt: time.Now().UTC(),
		Requester:   requester,
		Reason:      reason,
		When:        when,
	}
	if err := session.Write(*st); err != nil {
		return "", fmt.Errorf("recording the restart request: %w", err)
	}

	expanded := expandRestartHandoff(argv, principal, st.Key(), when, reason)
	if out, herr := restartHandoffFn(st.WorktreePath, expanded); herr != nil {
		st.RestartRequest = nil
		if werr := session.Write(*st); werr != nil {
			servelog.Errorf("restart clear-request-failed key=%s err=%v", st.Key(), werr)
		}
		return "", fmt.Errorf("the restart hand-off %q failed (%v), so nothing was queued: %s", expanded[0], herr, out)
	}
	if force {
		return fmt.Sprintf(
			"FORCED restart queued for %s (%s). The frontend restarts the session without waiting for the turn to end and without waiting for a running merge/check job, which it kills, so end your turn now and do not start new work.",
			st.Key(), reason), nil
	}
	return fmt.Sprintf(
		"restart queued for %s (%s). Nothing has been stopped yet: the frontend restarts the session once its current turn ends and the checks still pass, so end your turn now and do not start new work.",
		st.Key(), reason), nil
}

// restartThisSessionParams is the parameter set of restart-this-session.
type restartThisSessionParams struct {
	Reason string `json:"reason"`
	Force  bool   `json:"force"`
}

// rejectForceFor fails closed on any force argument, including false: the tool
// has no such parameter, and accepting one would invite a later wiring of it.
func rejectForceFor(args json.RawMessage, msg string) error {
	var raw map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if _, present := raw["force"]; present {
		return errors.New(msg)
	}
	return nil
}

// callerSession resolves the caller's own session the way close-this-session
// does (principal, frontend mapping, session record) and guards it.
func callerSession() (principal string, st *session.State, err error) {
	principal = currentPrincipal()
	key, err := confinement.SessionKeyOfPrincipal(principal)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", errNoRestartSession, err)
	}
	st, err = session.FindByTarget(key)
	if err != nil {
		return "", nil, err
	}
	if err := guardHandleTarget(st, "restart"); err != nil {
		return "", nil, err
	}
	return principal, st, nil
}

// runRestartThisSession is the restart-this-session tool body.
func runRestartThisSession(p restartThisSessionParams) (string, error) {
	principal, st, err := callerSession()
	if err != nil {
		return "", err
	}
	reason := p.Reason
	if reason == "" {
		reason = defaultSelfRestartReason
	}
	return queueRestart(st, principal, principal, reason, p.Force)
}

// handleRestartThisSession is the `restart-this-session` MCP tool handler.
func handleRestartThisSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var p restartThisSessionParams
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}
	text, err := runRestartThisSession(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

func restartThisSessionParamList() []command.Param {
	return []command.Param{
		{
			Name:        "reason",
			Type:        command.String,
			Description: "Why the restart is wanted; passed to the frontend and shown to the holders. Defaults to \"" + defaultSelfRestartReason + "\".",
		},
		forceRestartParam(),
	}
}

// restartChildParams is the parameter set of restart-child-session.
type restartChildParams struct {
	Child  string `json:"child"`
	Reason string `json:"reason"`
	Force  bool   `json:"force"`
}

// runRestartChild is the shared restart-child-session flow, shaped like
// runCloseChild: resolve the caller, resolve the child, check the handle and
// the restart right, then queue the restart on the child with the hand-off read
// from the child's own repo hierarchy.
func runRestartChild(p restartChildParams) (string, error) {
	if p.Child == "" {
		return "", errors.New("child is required")
	}
	child, callerPrincipal, err := authorizeRestartChild("restart-child-session", p.Child)
	if err != nil {
		return "", err
	}
	// The child's principal comes from the frontend mapping, not from state the
	// agent can write.
	childPrincipal, err := ownerPrincipal(child, "", "restart",
		"The operator can restart it from a terminal outside the session.")
	if err != nil {
		return "", err
	}
	reason := p.Reason
	if reason == "" {
		reason = defaultChildRestartReason
	}
	return queueRestart(child, callerPrincipal, childPrincipal, reason, p.Force)
}

// authorizeRestartChild resolves target and checks the caller may act on its
// restart: a handle (accept-on-first-use) that carries the restart right. tool
// names the verb in the refusal. It is shared by restart-child-session and
// cancel-restart with a child.
func authorizeRestartChild(tool, target string) (child *session.State, callerPrincipal string, err error) {
	callerPrincipal = currentPrincipal()
	callerKey := bestEffortSessionKey()

	child, err = findHandleTarget(target)
	if err != nil {
		return nil, "", err
	}
	// FDR 0032 handle guard, before the accept-on-first-use write below.
	if err := guardHandleTarget(child, "accept"); err != nil {
		return nil, "", err
	}
	if child.IsPendingHolder(callerPrincipal) {
		child.AcceptHandle(callerPrincipal)
		if err := session.Write(*child); err != nil {
			return nil, "", fmt.Errorf("accepting handle on %s: %w", child.Key(), err)
		}
	}
	if err := authorizeHandleUse(callerPrincipal, callerKey, *child); err != nil {
		return nil, "", err
	}
	if err := requireHandleRight(tool, "restart", callerPrincipal, *child); err != nil {
		return nil, "", err
	}
	return child, callerPrincipal, nil
}

// handleRestartChildSession is the `restart-child-session` MCP tool handler.
// Its authority is a handle read from agent-writable session state, so a
// confined caller is refused before anything else.
func handleRestartChildSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	if refusal := refuseConfinedServeCaller("restart-child-session", "sc restart-child-session"); refusal != nil {
		return refusal, nil
	}
	var p restartChildParams
	if err := json.Unmarshal(args, &p); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	text, err := runRestartChild(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

func restartChildSessionParamList() []command.Param {
	return []command.Param{
		{
			Name:        "child",
			Type:        command.String,
			Required:    true,
			Description: "The session to restart, as its <repo>/<branch> session key or its worktree directory name — exactly the strings `sc list` prints.",
			Completer:   completeWorktreeTargets,
		},
		{
			Name:        "reason",
			Type:        command.String,
			Description: "Why the restart is wanted; passed to the frontend and shown to the holders. Defaults to \"" + defaultChildRestartReason + "\".",
		},
		forceRestartParam(),
	}
}

// forceRestartParam is the escalation both restart tools share. It is
// always-ask in perms.AlwaysAsk, so no allow-list approves it silently.
func forceRestartParam() command.Param {
	return command.Param{
		Name:        "force",
		Type:        command.Bool,
		Description: "Restart NOW instead of when the turn ends, and even while a merge/check job runs. Forcing kills the session's current turn and any running job, and always prompts the human — no allow-list can approve it silently — so reach for it only when waiting is not an option.",
	}
}

// restartVerdict is the one line restart-preflight prints.
type restartVerdict struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
	When    string `json:"when"`
}

func noRestart(reason string) restartVerdict {
	return restartVerdict{Verdict: "none", Reason: reason}
}

// runRestartPreflight is what the frontend runs at a session's idle transition,
// before restarting it. An empty target means the session of this process's
// principal (CLOWN_SESSION_ID). It re-checks everything and CONSUMES the
// request, whatever the verdict, so a request is acted on at most once: a
// refusal clears it and tells the holders, and a restart verdict clears it so
// the relaunched session does not restart again. A non-nil error is an internal
// failure only; a refusal is a verdict.
func runRestartPreflight(target string) (restartVerdict, error) {
	implied := ""
	if target == "" {
		implied = currentPrincipal()
		key, kerr := confinement.SessionKeyOfPrincipal(implied)
		if kerr != nil {
			// The frontend runs this on every idle transition, mostly for sessions
			// with nothing queued; say why it did nothing, but do not fail it.
			servelog.Errorf("restart-preflight: %v", kerr)
			return noRestart("no spinclass session is tied to this principal"), nil
		}
		target = key
	}
	st, err := session.FindByTarget(target)
	if errors.Is(err, session.ErrTargetNotFound) {
		return noRestart("no spinclass session " + target), nil
	}
	if err != nil {
		return restartVerdict{}, err
	}
	switch {
	case st.RestartRequest == nil:
		return noRestart("no restart is queued for " + st.Key()), nil
	case st.Kind == session.KindImplicit:
		return noRestart(st.Key() + " is an implicit session"), nil
	case st.ResolveState() == session.StateAbandoned:
		return noRestart(st.Key() + " is closed"), nil
	}
	req := *st.RestartRequest

	consume := func() error {
		st.RestartRequest = nil
		return session.Write(*st)
	}
	principal, principalErr := ownerPrincipal(st, implied, "restart",
		fmt.Sprintf("Its holders (%s) are told; the operator can restart it from a terminal outside the session.", holdersOrNone(st.Holders)))
	refuse := func(reason string) (restartVerdict, error) {
		if werr := consume(); werr != nil {
			servelog.Errorf("restart-preflight clear-request-failed key=%s err=%v", st.Key(), werr)
		}
		notifyRestartRefused(st, principal, req, reason)
		return restartVerdict{Verdict: "refused", Reason: reason, When: req.When}, nil
	}
	if principalErr != nil {
		return refuse(principalErr.Error())
	}
	// A when=now request was force-approved by the operator at request time,
	// which included killing a running job, so the job check is skipped again.
	_, obstacle, err := restartPlan(st, principal, req.When == restartWhenNow)
	if err != nil {
		return restartVerdict{}, err
	}
	if obstacle != "" {
		return refuse(obstacle)
	}
	if err := consume(); err != nil {
		return restartVerdict{}, fmt.Errorf("consuming the restart request: %w", err)
	}
	return restartVerdict{
		Verdict: "restart",
		Reason:  fmt.Sprintf("restart of %s requested by %s: %s", st.Key(), req.Requester, req.Reason),
		When:    req.When,
	}, nil
}

// notifyRestartRefused tells the holders (and the requester, when it is not one
// of them) that a queued restart was refused. Best-effort: a wake failure must
// not change the verdict.
func notifyRestartRefused(st *session.State, principal string, req session.RestartRequest, reason string) {
	targets := st.OtherHolders(principal)
	if req.Requester != "" && req.Requester != principal && !slices.Contains(targets, req.Requester) {
		targets = append(targets, req.Requester)
	}
	if len(targets) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), restartNotifyTimeout)
	defer cancel()
	msg := fmt.Sprintf("restart of %s was refused and cleared: %s", st.Key(), reason)
	for _, t := range targets {
		if err := notifyPrincipalFn(ctx, t, "", msg); err != nil {
			servelog.Errorf("restart-preflight notify-failed key=%s target=%s err=%v", st.Key(), t, err)
		}
	}
}

// cancelRestartParams is the parameter set of cancel-restart.
type cancelRestartParams struct {
	Child string `json:"child"`
}

func cancelRestartParamList() []command.Param {
	return []command.Param{
		{
			Name:        "child",
			Type:        command.String,
			Description: "The session whose queued restart to cancel, as its <repo>/<branch> session key or its worktree directory name. Omit it to cancel THIS session's own queued restart. Naming a child needs the `restart` right on it, like restart-child-session.",
			Completer:   completeWorktreeTargets,
		},
	}
}

// runCancelRestart clears a queued RestartRequest, on the caller's own session
// (no child) or on a child the caller holds the restart right on. It does not
// call the hand-off: the frontend drops its pending restart when restart-preflight
// next answers "none". Nothing queued is a success, so the call is idempotent.
func runCancelRestart(p cancelRestartParams) (string, error) {
	var st *session.State
	var caller, sessionsOwnID string
	var err error
	if p.Child == "" {
		caller, st, err = callerSession()
		sessionsOwnID = caller
	} else {
		st, caller, err = authorizeRestartChild("cancel-restart", p.Child)
	}
	if err != nil {
		return "", err
	}
	if p.Child != "" {
		// Best effort: only used to avoid telling the session about its own
		// request, so a failure here just means it may be told.
		sessionsOwnID, _ = ownerPrincipal(st, "", "restart", "")
	}
	if st.RestartRequest == nil {
		return fmt.Sprintf("no restart is queued for %s, so there is nothing to cancel", st.Key()), nil
	}
	req := *st.RestartRequest
	st.RestartRequest = nil
	if err := session.Write(*st); err != nil {
		return "", fmt.Errorf("clearing the restart request: %w", err)
	}
	notifyRestartCancelled(st, req, caller, sessionsOwnID)
	return fmt.Sprintf(
		"cancelled the queued restart of %s (%s). The frontend drops it when its next restart-preflight answers \"none\".",
		st.Key(), req.Reason), nil
}

// notifyRestartCancelled tells the original requester, when it is neither the
// canceller nor the session itself, that its restart was cancelled. Best-effort.
func notifyRestartCancelled(st *session.State, req session.RestartRequest, caller, sessionsOwnID string) {
	if req.Requester == "" || req.Requester == caller || req.Requester == sessionsOwnID {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), restartNotifyTimeout)
	defer cancel()
	msg := fmt.Sprintf("the restart of %s that you requested (%s) was cancelled by %s", st.Key(), req.Reason, caller)
	if err := notifyPrincipalFn(ctx, req.Requester, "", msg); err != nil {
		servelog.Errorf("cancel-restart notify-failed key=%s target=%s err=%v", st.Key(), req.Requester, err)
	}
}

// handleCancelRestart is the `cancel-restart` MCP tool handler. Cancelling the
// caller's own restart is open; naming a child rests on a handle read from
// agent-writable session state, so that form refuses a confined caller.
func handleCancelRestart(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var p cancelRestartParams
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}
	if p.Child != "" {
		if refusal := refuseConfinedServeCaller("cancel-restart", "sc cancel-restart"); refusal != nil {
			return refusal, nil
		}
	}
	text, err := runCancelRestart(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

// printRestartVerdict writes v as exactly one JSON line.
func printRestartVerdict(w io.Writer, v restartVerdict) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// restartPreflightCommand registers the preflight as a hidden, CLI-only
// command: it is plumbing for the frontend, not something an agent calls.
func restartPreflightCommand() *command.Command {
	return &command.Command{
		Name:   "restart-preflight",
		Hidden: true,
		Description: command.Description{
			Short: "Check and consume a queued session restart (#419); prints one JSON verdict line",
		},
		Params: []command.Param{
			{Name: "target", Type: command.String, Description: "Session key or worktree name; defaults to the session of CLOWN_SESSION_ID"},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				Target string `json:"target"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &p); err != nil {
					return fmt.Errorf("invalid arguments: %w", err)
				}
			}
			v, err := runRestartPreflight(p.Target)
			if err != nil {
				return err
			}
			return printRestartVerdict(os.Stdout, v)
		},
	}
}
