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
	"code.linenisgreat.com/spinclass/internal/clown"
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
// runs and sessions that already exist get it. Force does not exist yet.

const (
	restartWhenIdle           = "idle"
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
// principal, the one {principal} expands to and clown restarts it by.
func restartPlan(st *session.State, principal string) (argv []string, obstacle string, err error) {
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

	if j, jerr := job.Read(st.WorktreePath); jerr == nil && j.Status == job.StatusRunning {
		return nil, fmt.Sprintf("a %s job (%s) is still running in %s; a restart would kill it, so let it finish or cancel it first", j.Kind, j.ID, st.Key()), nil
	}
	return argv, "", nil
}

// queueRestart records a RestartRequest on st and runs the hand-off. requester
// is the principal that asked; principal is st's own. A failed hand-off clears
// the request again, so the frontend never restarts on a request that was not
// handed off.
func queueRestart(st *session.State, requester, principal, reason string) (string, error) {
	argv, obstacle, err := restartPlan(st, principal)
	if err != nil {
		return "", err
	}
	if obstacle != "" {
		return "", errors.New(obstacle)
	}

	st.RestartRequest = &session.RestartRequest{
		RequestedAt: time.Now().UTC(),
		Requester:   requester,
		Reason:      reason,
		When:        restartWhenIdle,
	}
	if err := session.Write(*st); err != nil {
		return "", fmt.Errorf("recording the restart request: %w", err)
	}

	expanded := expandRestartHandoff(argv, principal, st.Key(), restartWhenIdle, reason)
	if out, herr := restartHandoffFn(st.WorktreePath, expanded); herr != nil {
		st.RestartRequest = nil
		if werr := session.Write(*st); werr != nil {
			servelog.Errorf("restart clear-request-failed key=%s err=%v", st.Key(), werr)
		}
		return "", fmt.Errorf("the restart hand-off %q failed (%v), so nothing was queued: %s", expanded[0], herr, out)
	}
	return fmt.Sprintf(
		"restart queued for %s (%s). Nothing has been stopped yet: the frontend restarts the session once its current turn ends and the checks still pass, so end your turn now and do not start new work.",
		st.Key(), reason), nil
}

// restartThisSessionParams is the parameter set of restart-this-session.
type restartThisSessionParams struct {
	Reason string `json:"reason"`
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

// runRestartThisSession is the restart-this-session tool body.
func runRestartThisSession(p restartThisSessionParams) (string, error) {
	principal := currentPrincipal()
	key, err := confinement.SessionKeyOfPrincipal(principal)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errNoRestartSession, err)
	}
	st, err := session.FindByTarget(key)
	if err != nil {
		return "", err
	}
	if err := guardHandleTarget(st, "restart"); err != nil {
		return "", err
	}
	reason := p.Reason
	if reason == "" {
		reason = defaultSelfRestartReason
	}
	return queueRestart(st, principal, principal, reason)
}

// handleRestartThisSession is the `restart-this-session` MCP tool handler.
func handleRestartThisSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	if err := rejectForceFor(args, "restart-this-session takes no force argument yet; it waits for the session to be idle and is refused while a job runs"); err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
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
	}
}

// restartChildParams is the parameter set of restart-child-session.
type restartChildParams struct {
	Child  string `json:"child"`
	Reason string `json:"reason"`
}

// runRestartChild is the shared restart-child-session flow, shaped like
// runCloseChild: resolve the caller, resolve the child, check the handle and
// the restart right, then queue the restart on the child with the hand-off read
// from the child's own repo hierarchy.
func runRestartChild(p restartChildParams) (string, error) {
	if p.Child == "" {
		return "", errors.New("child is required")
	}
	callerPrincipal := currentPrincipal()
	callerKey := bestEffortSessionKey()

	child, err := findHandleTarget(p.Child)
	if err != nil {
		return "", err
	}
	// FDR 0032 handle guard, before the accept-on-first-use write below.
	if err := guardHandleTarget(child, "accept"); err != nil {
		return "", err
	}
	if child.IsPendingHolder(callerPrincipal) {
		child.AcceptHandle(callerPrincipal)
		if err := session.Write(*child); err != nil {
			return "", fmt.Errorf("accepting handle on %s: %w", child.Key(), err)
		}
	}
	if err := authorizeHandleUse(callerPrincipal, callerKey, *child); err != nil {
		return "", err
	}
	if err := requireHandleRight("restart-child-session", "restart", callerPrincipal, *child); err != nil {
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
	return queueRestart(child, callerPrincipal, childPrincipal, reason)
}

// handleRestartChildSession is the `restart-child-session` MCP tool handler.
// Its authority is a handle read from agent-writable session state, so a
// confined caller is refused before anything else.
func handleRestartChildSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	if refusal := refuseConfinedServeCaller("restart-child-session", "sc restart-child-session"); refusal != nil {
		return refusal, nil
	}
	if err := rejectForceFor(args, "restart-child-session takes no force argument yet; it waits for the child to be idle and is refused while a job runs"); err != nil {
		return command.TextErrorResult(err.Error()), nil
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
	_, obstacle, err := restartPlan(st, principal)
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
