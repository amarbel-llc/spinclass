package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/0/clown"
	"code.linenisgreat.com/spinclass/internal/alfa/job"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	spinclose "code.linenisgreat.com/spinclass/internal/delta/close"
	"code.linenisgreat.com/spinclass/internal/servelog"
)

// close-this-session (#389) lets a session end itself. It is two halves:
//
//   - the MCP tool, run inside the session, which checks that the operator
//     allowed it and that closing is safe, then records a CloseRequest and
//     returns without touching anything;
//   - the runner (`close-self-run`), run OUTSIDE the session after its last
//     turn, which re-checks everything and does the teardown. The teardown
//     stops the session's agent unit, so it cannot run inside the tool call.
//
// Authority is a scope, not a handle: [permissions] allow-self-close in a
// sweatfile layer above the repo. A session therefore cannot grant it to
// itself, and it never closes any session but its own, so FDR 0032 D12's rule
// that a session only ever removes its own handle is untouched. Force is never
// available here: discarding work is the operator's act (`sc close --force`).

// errNoSelfSession is returned when the caller's principal maps to no session.
var errNoSelfSession = errors.New(
	"could not tie this call to a spinclass session, so there is nothing to close")

// selfCloseScope reports whether the operator allowed st to close itself.
func selfCloseScope(st *session.State) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, fmt.Errorf("resolving the home directory to read sweatfiles: %w", err)
	}
	h, err := sweatfileio.LoadHierarchy(home, st.RepoPath)
	if err != nil {
		return false, fmt.Errorf("reading the sweatfile hierarchy: %w", err)
	}
	allow, _ := sweatfileio.TrustedAllowSelfClose(h, home, st.RepoPath)
	return allow, nil
}

// selfCloseObstacle returns why st must not close right now, or "" when it may.
// It is the checks the tool and the runner share, so a request that passes here
// is one the runner will also accept unless something changed in between.
func selfCloseObstacle(st *session.State, principal string) (string, error) {
	allowed, err := selfCloseScope(st)
	if err != nil {
		return "", err
	}
	if !allowed {
		return fmt.Sprintf(
			"%s is not allowed to close itself: an operator must set `allow-self-close = true` under [permissions] in the global sweatfile or one above the repo (a repo or worktree sweatfile is ignored)",
			st.Key()), nil
	}

	if j, jerr := job.Read(st.WorktreePath); jerr == nil && j.Status == job.StatusRunning {
		return fmt.Sprintf("a %s job (%s) is still running in %s; let it finish or cancel it first", j.Kind, j.ID, st.Key()), nil
	}

	orphaned, err := soleHolderOf(st, principal)
	if err != nil {
		return "", err
	}
	if len(orphaned) > 0 {
		return fmt.Sprintf(
			"closing %s would leave %s with no accepted holder; grant a handle on %s first (grant-session-handle) or reap %s",
			st.Key(), strings.Join(orphaned, ", "), pluralIt(len(orphaned)), pluralIt(len(orphaned))), nil
	}

	blocker, err := spinclose.Blocker(st.RepoPath, st.WorktreePath, st.Branch)
	if err != nil {
		return "", err
	}
	if blocker != "" {
		return blocker + " Merge or commit it first; close-this-session never discards work.", nil
	}
	return "", nil
}

func pluralIt(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// soleHolderOf lists the live sessions other than st on which principal is the
// only accepted holder: closing st would orphan them.
func soleHolderOf(st *session.State, principal string) ([]string, error) {
	all, err := session.ListAll(nil)
	if err != nil {
		return nil, fmt.Errorf("listing sessions to check what closing would orphan: %w", err)
	}
	var keys []string
	for i := range all {
		s := &all[i]
		if s.Key() == st.Key() || s.Kind == session.KindImplicit || s.ResolveState() == session.StateAbandoned {
			continue
		}
		if s.IsHolder(principal) && len(s.Holders) == 1 {
			keys = append(keys, s.Key())
		}
	}
	return keys, nil
}

// closeThisSessionParams is the (deliberately empty) parameter set: there is
// no force, and the target is always the caller.
type closeThisSessionParams struct{}

// rejectForce fails closed on any force argument, including false: the tool has
// no such parameter, and accepting one would invite a later wiring of it.
func rejectForce(args json.RawMessage) error {
	return rejectForceFor(args, "close-this-session takes no force argument and never discards work; only the operator can, with `sc close --force`")
}

// runCloseThisSession is the tool body.
func runCloseThisSession(args json.RawMessage) (string, error) {
	if err := rejectForce(args); err != nil {
		return "", err
	}
	principal := currentPrincipal()
	key, err := confinement.SessionKeyOfPrincipal(principal)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errNoSelfSession, err)
	}
	st, err := session.FindByTarget(key)
	if err != nil {
		return "", err
	}
	if err := guardHandleTarget(st, "close"); err != nil {
		return "", err
	}

	obstacle, err := selfCloseObstacle(st, principal)
	if err != nil {
		return "", err
	}
	if obstacle != "" {
		return "", errors.New(obstacle)
	}

	request := &session.CloseRequest{RequestedAt: time.Now().UTC(), Principal: principal}
	if err := updateLoadedSession(st, func(cur *session.State) bool {
		cur.CloseRequest = request
		return true
	}); err != nil {
		return "", fmt.Errorf("recording the close request: %w", err)
	}
	return fmt.Sprintf(
		"close queued for %s. Nothing has been removed yet: the teardown runs after this session's last turn, so end your turn now and do not start new work. If the teardown is refused, the holders are told and the session is left as it is.",
		st.Key()), nil
}

// handleCloseThisSession is the `close-this-session` MCP tool handler.
func handleCloseThisSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	text, err := runCloseThisSession(args)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

// runCloseSelfRun is the runner: it closes session target if, and only if, it
// still carries a close request and every check still passes. An empty target
// means the session of this process's principal (CLOWN_SESSION_ID), which is
// what clown's frontend has in its environment. A target with no session, or
// with no request, is a no-op so the caller may run it unconditionally on every
// idle transition.
//
// A refusal or failure clears the request (so it is not retried forever), tells
// the holders with an ExitFailed wake, and leaves the session whole.
func runCloseSelfRun(target string) error {
	implied := ""
	if target == "" {
		implied = currentPrincipal()
		key, kerr := confinement.SessionKeyOfPrincipal(implied)
		if kerr != nil {
			// The runner fires on every idle transition, mostly for sessions with
			// nothing queued; say why it did nothing, but do not fail the caller.
			servelog.Errorf("close-self-run: %v", kerr)
			return nil
		}
		target = key
	}
	st, err := session.FindByTarget(target)
	if errors.Is(err, session.ErrTargetNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.CloseRequest == nil || st.Kind == session.KindImplicit || st.ResolveState() == session.StateAbandoned {
		return nil
	}
	principal, principalErr := closeRunnerPrincipal(st, implied)

	fail := func(reason error) error {
		if werr := updateLoadedSession(st, func(cur *session.State) bool {
			cur.CloseRequest = nil
			return true
		}); werr != nil {
			servelog.Errorf("close-self-run clear-request-failed key=%s err=%v", st.Key(), werr)
		}
		if werr := emitExitWakesFn(st.OtherHolders(principal), st.Key(), clown.ExitFailed); werr != nil {
			servelog.Errorf("close-self-run emitExitWakesFn-failed key=%s err=%v", st.Key(), werr)
		}
		return reason
	}
	if principalErr != nil {
		return fail(principalErr)
	}

	obstacle, err := selfCloseObstacle(st, principal)
	if err != nil {
		return fail(err)
	}
	if obstacle != "" {
		return fail(errors.New(obstacle))
	}

	holders := st.OtherHolders(principal)
	key := st.Key()
	var buf bytes.Buffer
	if err := spinclose.RunResolved(&buf, st.RepoPath, st.WorktreePath, st.Branch, false, nil, "tap"); err != nil {
		return fail(err)
	}
	if err := emitExitWakesFn(holders, key, clown.ExitShutdown); err != nil {
		servelog.Errorf("close-self-run emitExitWakesFn-failed key=%s err=%v", key, err)
	}
	return nil
}

// closeRunnerPrincipal is whose close the runner carries out. It never reads
// st.CloseRequest.Principal: the session state lives in the worktree, which a
// confined agent can rewrite, and the principal decides the orphan check and
// who is woken. A frontend principal mapping naming the session wins. A
// session that was launched confined but has no mapping is refused. Only an
// unconfined session (no unit record or spec) falls back, first to the
// principal the runner itself resolved the session from (implied, its own
// CLOWN_SESSION_ID), then to the session's recorded principal; an unconfined
// agent is not inside any boundary that state could cross.
func closeRunnerPrincipal(st *session.State, implied string) (string, error) {
	return ownerPrincipal(st, implied, "close", fmt.Sprintf(
		"Its holders (%s) are told; the operator can close it with `sc close %s`",
		holdersOrNone(st.Holders), st.Key()))
}

// ownerPrincipal is closeRunnerPrincipal's resolution, shared with the restart
// verbs (#419): act names what is being done ("close", "restart") and refusal
// is the closing sentence of the two refusals, saying who is told and what the
// operator can do instead.
func ownerPrincipal(st *session.State, implied, act, refusal string) (string, error) {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return "", fmt.Errorf("resolving the frontend state directory: %w", err)
	}
	p, found, err := confinement.PrincipalOfSession(confinement.Host{}, stateDir, st.Key(), st.WorktreePath)
	if err != nil {
		return "", err
	}
	if found {
		return p, nil
	}
	if confinement.LaunchedConfined(stateDir, st.WorktreePath) {
		return "", fmt.Errorf("%s was launched confined but no frontend principal mapping names it, so whose %s this is cannot be told; refusing. %s", st.Key(), act, refusal)
	}
	if implied != "" {
		return implied, nil
	}
	if st.Principal != "" {
		return st.Principal, nil
	}
	return "", fmt.Errorf("%s records no principal, so whose %s this is cannot be told; refusing. %s", st.Key(), act, refusal)
}

func holdersOrNone(holders []string) string {
	if len(holders) == 0 {
		return "none"
	}
	return strings.Join(holders, ", ")
}

// closeSelfRunCommand registers the runner as a hidden, CLI-only command: it is
// plumbing for the frontend, not something an agent calls.
func closeSelfRunCommand() *command.Command {
	return &command.Command{
		Name:   "close-self-run",
		Hidden: true,
		Description: command.Description{
			Short: "Run a session's queued self-close (#389); a no-op without a request",
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
			return runCloseSelfRun(p.Target)
		},
	}
}
