package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
	"golang.org/x/term"
)

// `sc restart SESSION... --posture P` (spinclass#473) is the operator's restart:
// the same request path as restart-child-session (restartPlan, postureObstacle,
// queueRestartPosture and the trusted [session-entry].restart hand-off), but the
// authority is the human at the terminal, not a handle. It therefore works for
// any session the operator can see and checks no handle or restart right.
//
// It refuses outright unless stdin and stdout are both terminals: a restart
// kills another session's turn when that turn ends, and an agent (whose shell
// is a pipe) must not be able to drive this verb. Under the same uid this is a
// speed bump, not a boundary, as for `sc spawn --unconfined` and `sc grit`. It is
// CLI-only by construction (RunCLI, no Run), so it is never an MCP tool.
//
// Every target is judged on its own, like multi-target `sc close`: each prints
// one verdict line with its old and new posture, one failing target does not
// stop the rest, and the exit is nonzero if any failed. A widening (attended)
// prompts once for the whole batch, default no; the trusted
// allow-posture-widening key is still required. There is no force.

// Seams for command tests.
var (
	operatorRestartIsTerminal = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	}
	operatorRestartConfirmIn = func() io.Reader { return os.Stdin }
	operatorRestartStdout    = func() io.Writer { return os.Stdout }
	operatorRestartStderr    = func() io.Writer { return os.Stderr }
)

const (
	// operatorRestartRequester is recorded as the request's requester: the
	// shared marker the preflight and --rearm accept (see its definition for
	// how soft it is). The operator at a terminal has no principal.
	operatorRestartRequester     = session.OperatorRestartRequester
	defaultOperatorRestartReason = "requested by the operator (sc restart)"
	// postureUnknown is printed for a session whose posture cannot be read: no
	// session, or one with no pinned entry.
	postureUnknown      = "unknown"
	declinedAtThePrompt = "declined at the prompt"
)

type operatorRestartParams struct {
	Target  []string `json:"target"`
	Posture string   `json:"posture"`
	Reason  string   `json:"reason"`
}

// operatorRestartTarget is one target's judgement before anything is written.
type operatorRestartTarget struct {
	name      string // the session key, or the target as typed when none resolved
	old       string
	st        *session.State
	principal string
	refusal   string
}

// pinnedPosture is the posture a session's pinned entry records: unattended
// when its env carries the CLOWN_UNATTENDED marker, attended when it does not,
// unknown with no pinned entry.
func pinnedPosture(st *session.State) string {
	switch {
	case st == nil || st.Entry == nil:
		return postureUnknown
	case spawn.HasUnattendedMarker(st.Entry.Env):
		return restartPostureUnattended
	}
	return restartPostureAttended
}

// judgeOperatorRestart resolves target and runs every check the request path
// runs, so the prompt lists only widenings that can happen. It writes nothing.
func judgeOperatorRestart(target, posture string) operatorRestartTarget {
	j := operatorRestartTarget{name: target, old: postureUnknown}
	if err := rejectRemoteTarget(target, remotesForTarget(target)); err != nil {
		j.refusal = err.Error()
		return j
	}
	st, err := session.FindByTarget(target)
	if err != nil {
		j.refusal = err.Error()
		return j
	}
	j.name, j.old, j.st = st.Key(), pinnedPosture(st), st
	if st.IsTombstone() || st.ResolveState() == session.StateAbandoned {
		j.refusal = st.Key() + " is closed; nothing to restart"
		return j
	}
	if obstacle := postureObstacle(st, posture); obstacle != "" {
		j.refusal = obstacle
		return j
	}
	// The session's own principal comes from the frontend mapping, as for
	// restart-child-session.
	j.principal, err = ownerPrincipal(st, "", "restart", "Nothing was queued.")
	if err != nil {
		j.refusal = err.Error()
		return j
	}
	if _, obstacle, perr := restartPlan(st, j.principal, false); perr != nil {
		j.refusal = perr.Error()
	} else if obstacle != "" {
		j.refusal = obstacle
	}
	return j
}

// confirmOperatorWidening asks once, on the terminal, before every widening in
// the batch. Only y or yes approves; anything else, an empty line included, is
// no.
func confirmOperatorWidening(widenings []operatorRestartTarget) bool {
	w := operatorRestartStderr()
	fmt.Fprintln(w, "These sessions would be restarted ATTENDED (a widening: a human answers their permission prompts):")
	for _, j := range widenings {
		fmt.Fprintf(w, "  %s (%s -> %s)\n", j.name, j.old, restartPostureAttended)
	}
	fmt.Fprintf(w, "widen %d session(s)? [y/N] ", len(widenings))
	line, _ := bufio.NewReader(operatorRestartConfirmIn()).ReadString('\n')
	if !strings.HasSuffix(line, "\n") {
		fmt.Fprintln(w)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// runOperatorRestart is `sc restart`.
func runOperatorRestart(p operatorRestartParams) error {
	if !operatorRestartIsTerminal() {
		return errors.New("restart: stdin and stdout must both be terminals; restarting a session is the operator's act and cannot be piped or driven by a program (an agent asks with restart-child-session)")
	}
	if len(p.Target) == 0 {
		return errors.New("restart: name at least one session (a <repo>/<branch> session key or worktree name from `sc list`)")
	}
	if p.Posture == "" {
		return errors.New("restart: --posture is required: unattended, attended or keep")
	}
	posture, err := normalizePosture(p.Posture)
	if err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	reason := p.Reason
	if reason == "" {
		reason = defaultOperatorRestartReason
	}

	judged := make([]operatorRestartTarget, len(p.Target))
	var widenings []operatorRestartTarget
	for i, t := range p.Target {
		judged[i] = judgeOperatorRestart(t, posture)
		if judged[i].refusal == "" && posture == restartPostureAttended {
			widenings = append(widenings, judged[i])
		}
	}
	if len(widenings) > 0 && !confirmOperatorWidening(widenings) {
		for i := range judged {
			if judged[i].refusal == "" {
				judged[i].refusal = declinedAtThePrompt
			}
		}
	}

	out := operatorRestartStdout()
	failed := 0
	for _, j := range judged {
		newPosture := posture
		if newPosture == "" {
			newPosture = j.old
		}
		refusal := j.refusal
		if refusal == "" {
			if _, qerr := queueRestartPosture(j.st, operatorRestartRequester, j.principal, reason, false, posture); qerr != nil {
				refusal = qerr.Error()
			}
		}
		if refusal != "" {
			failed++
			fmt.Fprintf(out, "%s: %s -> %s: refused: %s\n", j.name, j.old, newPosture, refusal)
			continue
		}
		fmt.Fprintf(out, "%s: %s -> %s: restart requested\n", j.name, j.old, newPosture)
	}
	if failed > 0 {
		return fmt.Errorf("restart: %d of %d session(s) not restarted", failed, len(judged))
	}
	return nil
}

// registerOperatorRestartCommand registers `sc restart`, CLI-only.
func registerOperatorRestartCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "restart",
		Title: "Restart sessions as the operator",
		Description: command.Description{
			Short: "Restart one or more sessions into a permission posture, as the operator at a terminal (spinclass#473, FDR 0037)",
			Long: "Asks the harness frontend to stop and relaunch each named session, keeping its identity, through the same request and " +
				"trusted [session-entry].restart hand-off that restart-child-session uses. The operator is the authority: any session " +
				"`sc list` shows can be named, and no handle or restart right is needed. The request is recorded with the requester " +
				"\"" + operatorRestartRequester + "\"; the restart itself happens when the session next ends a turn. " +
				"--posture unattended relaunches into claude's dontAsk mode (a narrowing, no prompt); attended relaunches out of it " +
				"(a widening: it needs `allow-posture-widening = true` under [session-entry] in the global sweatfile or one above the " +
				"repo, and asks once for the whole batch on the terminal, listing every session to widen, default no); keep restarts " +
				"with no posture change. The posture is recorded in the session's pinned entry when the restart starts, so the next " +
				"`sc resume` agrees. A session launched confined is refused for unattended and attended (its posture is its " +
				"confinement profile's), as is one with no pinned entry (one that predates pinning: resume it once, which pins it) and, " +
				"for unattended, one whose pinned entry is not a claude harness. The usual restart refusals apply too: no hand-off " +
				"configured, a running merge/check job, an implicit session. There is no force: the restart waits for the turn to end. " +
				"It refuses to run unless stdin and stdout are both terminals, so an agent cannot drive it. " +
				"Each session is judged on its own: every one prints a line \"<session>: <old> -> <new>: restart requested\" or " +
				"\"<session>: <old> -> <new>: refused: <reason>\", where a posture is unattended, attended or unknown (no session or no " +
				"pinned entry, read from the pinned entry's CLOWN_UNATTENDED marker); one failure does not stop the rest, and the exit " +
				"is nonzero if any session was not restarted. A queued restart can be withdrawn with cancel-restart.",
		},
		Params: []command.Param{
			{Name: "target", Type: command.String, Variadic: true, Description: "Sessions to restart (at least one): <repo>/<branch> session keys or worktree directory names, exactly as `sc list` prints them", Completer: completeWorktreeTargets},
			{Name: "posture", Type: command.String, Description: "Required: unattended (claude's dontAsk mode; a narrowing), attended (a widening: needs the trusted allow-posture-widening key and a y at the prompt) or keep (no posture change)"},
			{Name: "reason", Type: command.String, Description: "Why the restart is wanted; passed to the frontend and shown to the holders. Defaults to \"" + defaultOperatorRestartReason + "\""},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p operatorRestartParams
			if err := json.Unmarshal(args, &p); err != nil {
				return fmt.Errorf("restart: invalid arguments: %w", err)
			}
			return runOperatorRestart(p)
		},
	})
}
