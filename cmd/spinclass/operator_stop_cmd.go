package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// `sc stop` and `sc start-stopped` are the operator's stop and start
// (docs/plans/2026-11-20-stop-start-rights.md): the same request path as
// stop-child-session (queueStop) and start-child-session (startStoppedSession),
// with the human at the terminal as the authority instead of a handle, so no
// handle or right is checked. Like `sc restart` (spinclass#473) they refuse
// unless stdin and stdout are terminals and are CLI-only by construction. Every
// target is judged before anything is written, one prompt (default no) covers
// the batch, every target prints one verdict line, one failure does not stop
// the rest, and the exit is nonzero if any failed. They share sc restart's
// terminal seams.
//
// A target names a session. The form SESSION@<principal>, one clown of a
// session that runs more than one, is reserved: it is refused today so the
// grammar can grow without changing what a plain session target means.

const (
	defaultOperatorStopReason = "requested by the operator (sc stop)"
	stopVerb                  = "stop"
	startStoppedVerb          = "start-stopped"
)

type operatorStopParams struct {
	Target    []string `json:"target"`
	AllOnHost bool     `json:"all-on-host"`
	Except    []string `json:"except"`
	Now       bool     `json:"now"`
	Reason    string   `json:"reason"`
}

type operatorStartParams struct {
	Target  []string `json:"target"`
	Message string   `json:"message"`
}

// operatorSessionTarget is one target's judgement before anything is written.
type operatorSessionTarget struct {
	name      string // the session key, or the target as typed when none resolved
	st        *session.State
	principal string
	argv      []string // start only: the trusted start hand-off
	refusal   string
	skip      string // not a failure
}

func (t operatorSessionTarget) pending() bool { return t.refusal == "" && t.skip == "" }

// resolveOperatorSessionTarget finds the session a target names, refusing the
// reserved per-clown form and remote targets.
func resolveOperatorSessionTarget(target string) (*session.State, string) {
	if strings.Contains(target, "@") {
		return nil, fmt.Sprintf("%s names one clown of a session, which is not supported yet: name the session (every clown it runs is addressed)", target)
	}
	if err := rejectRemoteTarget(target, remotesForTarget(target)); err != nil {
		return nil, err.Error()
	}
	st, err := session.FindByTarget(target)
	if err != nil {
		return nil, err.Error()
	}
	return st, ""
}

func sessionIsClosed(st *session.State) bool {
	return st.IsTombstone() || st.ResolveState() == session.StateAbandoned
}

// judgeOperatorStop runs every check the stop path runs, so the prompt lists
// only stops that can be queued.
func judgeOperatorStop(st *session.State, now bool) operatorSessionTarget {
	j := operatorSessionTarget{name: st.Key(), st: st}
	switch {
	case sessionIsClosed(st):
		j.refusal = st.Key() + " is closed; nothing to stop"
		return j
	case st.StoppedAt != nil:
		j.refusal = fmt.Sprintf("%s is already stopped (since %s, by %s)", st.Key(), st.StoppedAt.UTC().Format(time.RFC3339), st.StoppedBy)
		return j
	}
	principal, err := ownerPrincipal(st, "", "stop", "Nothing was queued.")
	if err != nil {
		j.refusal = err.Error()
		return j
	}
	j.principal = principal
	if _, obstacle, perr := restartPlan(st, principal, now); perr != nil {
		j.refusal = perr.Error()
	} else if obstacle != "" {
		j.refusal = obstacle
	}
	return j
}

// selectOperatorStopTargets judges the named sessions, or every live worktree
// session on the host minus --except and the caller's own session.
func selectOperatorStopTargets(p operatorStopParams) []operatorSessionTarget {
	if !p.AllOnHost {
		out := make([]operatorSessionTarget, len(p.Target))
		for i, t := range p.Target {
			st, refusal := resolveOperatorSessionTarget(t)
			if refusal != "" {
				out[i] = operatorSessionTarget{name: t, refusal: refusal}
				continue
			}
			out[i] = judgeOperatorStop(st, p.Now)
		}
		return out
	}
	all, err := session.ListAll(nil)
	if err != nil {
		return []operatorSessionTarget{{name: "(all sessions)", refusal: fmt.Sprintf("listing sessions: %v", err)}}
	}
	own := os.Getenv("SPINCLASS_SESSION_ID")
	var out []operatorSessionTarget
	for i := range all {
		st := &all[i]
		j := operatorSessionTarget{name: st.Key(), st: st}
		switch {
		case st.Kind == session.KindImplicit:
			j.skip = "a main-checkout (implicit) session"
		case sessionIsClosed(st):
			j.skip = "closed"
		case own != "" && st.Key() == own:
			j.skip = "this is the caller's own session"
		case slices.Contains(p.Except, st.Key()) || slices.Contains(p.Except, filepath.Base(st.WorktreePath)):
			j.skip = "excepted (--except)"
		case st.StoppedAt != nil:
			j.skip = "already stopped"
		default:
			j = judgeOperatorStop(st, p.Now)
		}
		out = append(out, j)
	}
	return out
}

// confirmOperatorBatch prints the plan and asks once, on the terminal. Only y
// or yes approves.
func confirmOperatorBatch(header string, lines []string, question string) bool {
	w := operatorRestartStderr()
	fmt.Fprintln(w, header)
	for _, l := range lines {
		fmt.Fprintln(w, "  "+l)
	}
	fmt.Fprint(w, question+" [y/N] ")
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

// promptOperatorBatch asks for the pending targets and marks them declined on
// a no.
func promptOperatorBatch(judged []operatorSessionTarget, header, verb string, detail func(operatorSessionTarget) string) {
	var lines []string
	for _, j := range judged {
		if j.pending() {
			lines = append(lines, detail(j))
		}
	}
	if len(lines) == 0 {
		return
	}
	if confirmOperatorBatch(header, lines, fmt.Sprintf("%s %d session(s)?", verb, len(lines))) {
		return
	}
	for i := range judged {
		if judged[i].pending() {
			judged[i].refusal = declinedAtThePrompt
		}
	}
}

// printOperatorVerdicts writes one line per target, applying act to each
// pending one, and returns how many were not done.
func printOperatorVerdicts(w io.Writer, judged []operatorSessionTarget, act func(operatorSessionTarget) (string, error)) (failed int) {
	for _, j := range judged {
		switch {
		case j.refusal != "":
			failed++
			fmt.Fprintf(w, "%s: refused: %s\n", j.name, j.refusal)
		case j.skip != "":
			fmt.Fprintf(w, "%s: skipped: %s\n", j.name, j.skip)
		default:
			done, err := act(j)
			if err != nil {
				failed++
				fmt.Fprintf(w, "%s: refused: %s\n", j.name, err)
				continue
			}
			fmt.Fprintf(w, "%s: %s\n", j.name, done)
		}
	}
	return failed
}

// runOperatorStop is `sc stop`.
func runOperatorStop(p operatorStopParams) error {
	if !operatorRestartIsTerminal() {
		return errors.New("stop: stdin and stdout must both be terminals; stopping a session is the operator's act and cannot be piped or driven by a program (an agent asks with stop-child-session)")
	}
	if p.AllOnHost == (len(p.Target) > 0) {
		return errors.New("stop: name at least one session, or pass --all-on-host (not both)")
	}
	if len(p.Except) > 0 && !p.AllOnHost {
		return errors.New("stop: --except only applies with --all-on-host")
	}
	when, done := restartWhenIdle, "stop requested"
	if p.Now {
		when, done = restartWhenNow, "stop NOW requested"
	}
	reason := p.Reason
	if reason == "" {
		reason = defaultOperatorStopReason
	}

	judged := selectOperatorStopTargets(p)
	header := "These sessions' agents would be stopped when they next end a turn, and left stopped until started again:"
	if p.Now {
		header = "These sessions' agents would be stopped NOW, killing their current turn and any running merge/check job, and left stopped until started again:"
	}
	promptOperatorBatch(judged, header, stopVerb, func(j operatorSessionTarget) string { return j.name })

	failed := printOperatorVerdicts(operatorRestartStdout(), judged, func(j operatorSessionTarget) (string, error) {
		if _, err := queueStop(j.st, operatorRestartRequester, j.principal, reason, when); err != nil {
			return "", err
		}
		return done, nil
	})
	if failed > 0 {
		return fmt.Errorf("stop: %d of %d session(s) not stopped", failed, len(judged))
	}
	return nil
}

// judgeOperatorStart runs every check the start path runs.
func judgeOperatorStart(target string) operatorSessionTarget {
	st, refusal := resolveOperatorSessionTarget(target)
	if refusal != "" {
		return operatorSessionTarget{name: target, refusal: refusal}
	}
	j := operatorSessionTarget{name: st.Key(), st: st}
	if sessionIsClosed(st) {
		j.refusal = st.Key() + " is closed; nothing to start"
		return j
	}
	if st.StoppedAt == nil {
		j.refusal = st.Key() + " is not stopped, so there is nothing to start (stop it first, or restart it with sc restart)"
		return j
	}
	principal, err := ownerPrincipal(st, "", "start", "Nothing was started.")
	if err != nil {
		j.refusal = err.Error()
		return j
	}
	argv, err := startHandoffFor(st)
	if err != nil {
		j.refusal = err.Error()
		return j
	}
	j.principal, j.argv = principal, argv
	return j
}

// runOperatorStart is `sc start-stopped`.
func runOperatorStart(p operatorStartParams) error {
	if !operatorRestartIsTerminal() {
		return errors.New("start-stopped: stdin and stdout must both be terminals; starting a session is the operator's act and cannot be piped or driven by a program (an agent asks with start-child-session)")
	}
	if strings.TrimSpace(p.Message) == "" {
		return errors.New("start-stopped: --message is required: a started session reads it as its first message, after a notice of the stop and the start")
	}
	if len(p.Target) == 0 {
		return errors.New("start-stopped: name at least one stopped session (a <repo>/<branch> session key or worktree name from `sc list`)")
	}
	judged := make([]operatorSessionTarget, len(p.Target))
	for i, t := range p.Target {
		judged[i] = judgeOperatorStart(t)
	}
	promptOperatorBatch(judged, "These stopped sessions would be started again, resumed with your message as their first:", "start",
		func(j operatorSessionTarget) string {
			return fmt.Sprintf("%s (stopped since %s by %s: %s)", j.name, j.st.StoppedAt.UTC().Format(time.RFC3339), j.st.StoppedBy, j.st.StoppedReason)
		})
	failed := printOperatorVerdicts(operatorRestartStdout(), judged, func(j operatorSessionTarget) (string, error) {
		if _, err := startStoppedSession(j.st, operatorRestartRequester, j.principal, j.argv, p.Message); err != nil {
			return "", err
		}
		return "started", nil
	})
	if failed > 0 {
		return fmt.Errorf("start-stopped: %d of %d session(s) not started", failed, len(judged))
	}
	return nil
}

// registerOperatorStopCommands registers `sc stop` and `sc start-stopped`,
// CLI-only.
func registerOperatorStopCommands(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  stopVerb,
		Title: "Stop sessions' agents as the operator",
		Description: command.Description{
			Short: "Stop one or more sessions' agents and leave them stopped, as the operator at a terminal",
			Long: "Asks the harness frontend to stop each named session's agent and not relaunch it, through the same stop request and " +
				"trusted [session-entry].restart hand-off that stop-child-session uses. The operator is the authority: any session `sc list` " +
				"shows can be named (or every live worktree session with --all-on-host, minus --except and the caller's own session), and no " +
				"handle or right is needed. The request is recorded with the requester \"" + operatorRestartRequester + "\". By default the " +
				"stop happens when the session next ends a turn and no merge/check job runs; --now stops it at once, killing the current " +
				"turn and any running job. A stopped session stays stopped until it is started again (sc start-stopped, or " +
				"start-child-session). Every session is judged first; one prompt (default no) covers the batch. Each prints " +
				"\"<session>: stop requested\", \"<session>: stop NOW requested\", \"<session>: skipped: <why>\" or " +
				"\"<session>: refused: <why>\"; one failure does not stop the rest, and the exit is nonzero if any session was refused. " +
				"A target of the form SESSION@<principal> (one clown of a session) is reserved and refused. It refuses to run unless stdin " +
				"and stdout are both terminals, so an agent cannot drive it.",
		},
		Params: []command.Param{
			{Name: "target", Type: command.String, Variadic: true, Description: "Sessions to stop: <repo>/<branch> session keys or worktree directory names, exactly as `sc list` prints them (or use --all-on-host)", Completer: completeWorktreeTargets},
			{Name: "all-on-host", Type: command.Bool, Description: "Every live worktree session on this host instead of named sessions; the caller's own session is skipped"},
			{Name: "except", Type: command.String, Variadic: true, Description: "With --all-on-host: sessions to leave running, as session keys or worktree directory names"},
			{Name: "now", Type: command.Bool, Description: "Stop at once, killing the current turn and any running merge/check job, instead of when the turn ends"},
			{Name: "reason", Type: command.String, Description: "Why the stop is wanted; shown to the holders and in the started session's notice. Defaults to \"" + defaultOperatorStopReason + "\""},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p operatorStopParams
			if err := json.Unmarshal(args, &p); err != nil {
				return fmt.Errorf("stop: invalid arguments: %w", err)
			}
			return runOperatorStop(p)
		},
	})
	app.AddCommand(&command.Command{
		Name:  startStoppedVerb,
		Title: "Start stopped sessions as the operator",
		Description: command.Description{
			Short: "Start one or more stopped sessions again with a first message, as the operator at a terminal",
			Long: "Runs the trusted [session-entry].start-handoff for each named stopped session, exactly as start-child-session does: the " +
				"same principal is relaunched detached with its conversation resumed, and its first message is a notice of who stopped it, " +
				"when and why, and that the operator started it, followed by --message. The operator is the authority: no handle or start " +
				"right is needed. (`sc start` creates a new session; this verb starts a stopped one.) Every session is judged first; one " +
				"prompt (default no) covers the batch. Each prints \"<session>: started\" or \"<session>: refused: <why>\"; one failure does " +
				"not stop the rest, and the exit is nonzero if any session was refused. It refuses to run unless stdin and stdout are both " +
				"terminals, so an agent cannot drive it.",
		},
		Params: []command.Param{
			{Name: "target", Type: command.String, Variadic: true, Description: "Stopped sessions to start (at least one): <repo>/<branch> session keys or worktree directory names, exactly as `sc list` prints them", Completer: completeWorktreeTargets},
			{Name: "message", Type: command.String, Required: true, Description: "Required: each started session's first message, after the notice of the stop and the start"},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p operatorStartParams
			if err := json.Unmarshal(args, &p); err != nil {
				return fmt.Errorf("start-stopped: invalid arguments: %w", err)
			}
			return runOperatorStart(p)
		},
	})
}
