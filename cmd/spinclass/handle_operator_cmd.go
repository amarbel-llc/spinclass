package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	tap "code.linenisgreat.com/tap/go/pkgs/writer"
	"golang.org/x/term"
)

// `sc handle-grant` and `sc handle-move` (spinclass#478, design in
// docs/plans/2026-11-12-478-operator-handle-command.md) add a handle to, or move
// the handles of, sessions as the operator at a terminal. The authority is the
// human, not a handle: the operator can name any live worktree session, which
// is exactly what grant-session-handle cannot do for a session whose spawner is
// gone or that `sc start` made. Like `sc restart` it refuses unless stdin and
// stdout are terminals (an agent's shell is a pipe), is CLI-only by
// construction, judges every session before writing anything, asks once for the
// whole batch (default no), prints one TAP line per session, does not let one
// failure stop the rest, and exits nonzero if any failed. Under the same uid
// the terminal guard is a speed bump, not a boundary.
//
// It never grants more than grant-session-handle's vocabulary and default
// (observe,close); the dangerous rights must be named and are listed in the
// prompt. A grant is effective when recorded (spinclass#480, amending FDR 0032
// D12): it is journaled first and the recipient and the other holders are
// woken. GrantedBy records the marker "operator"; no signature is written (the
// card act is out of scope for slice 0).

// Seams for command tests.
var (
	operatorHandleIsTerminal = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	}
	operatorHandleConfirmIn = func() io.Reader { return os.Stdin }
	operatorHandleStdout    = func() io.Writer { return os.Stdout }
	operatorHandleStderr    = func() io.Writer { return os.Stderr }
	// operatorHandleSessionOf resolves a principal to its live session key
	// through the frontend principal mapping, as the other handle flows do.
	operatorHandleSessionOf = confinement.SessionKeyOfPrincipal
	operatorHandleNow       = func() time.Time { return time.Now().UTC() }
)

const (
	operatorHandleGrant = "grant"
	operatorHandleMove  = "move"
)

// dangerousHandleRights are the rights past the conservative default; each one
// is listed in the batch prompt so the operator sees what is being given
// (spinclass#478 Q2). merge and check run the pre-merge hook, which executes
// arbitrary code on the host.
var dangerousHandleRights = map[string]string{
	"grant":    "pass handles on to other principals",
	"merge":    "land the session's work (runs its pre-merge hook: arbitrary code)",
	"check":    "gate the session's work (runs its pre-merge hook: arbitrary code)",
	"restart":  "have the session stopped and relaunched",
	"instruct": "give the session orders within its brief",
	"cap":      "cap the session",
}

type operatorHandleParams struct {
	Target       []string `json:"target"`
	To           string   `json:"to"`
	Rights       string   `json:"rights"`
	AllOnHost    bool     `json:"all-on-host"`
	DryRun       bool     `json:"dry-run"`
	ForceUnknown bool     `json:"force-unknown"`
}

// operatorHandleRecipient is the normalized --to.
type operatorHandleRecipient struct {
	principal string // the key stored in the handle lists
	sessionOf string // the live session this principal runs, "" when none is known
	note      string // printed once when the recipient could not be verified
}

// resolveOperatorHandleRecipient normalizes --to and checks it against a live
// session (Q6). A bare key, or a qualified <key>@<host> whose key resolves to a
// live local session, is stored as the bare key (today's storage key). A
// qualified key that resolves to nothing here is a remote principal: stored
// as given and unverified (the D8 verifier does not exist yet), so it, like
// an unknown bare key, needs --force-unknown.
func resolveOperatorHandleRecipient(to string, forceUnknown bool) (operatorHandleRecipient, error) {
	if to == "" {
		return operatorHandleRecipient{}, errors.New("--to is required: the principal key (a chat JID localpart, as `chat_list` and whoami show it)")
	}
	if strings.ContainsAny(to, " \t\r\n/\\") {
		return operatorHandleRecipient{}, fmt.Errorf("--to %q is not a principal key", to)
	}
	local, host, qualified := strings.Cut(to, "@")
	if qualified && (local == "" || host == "" || strings.Contains(host, "@")) {
		return operatorHandleRecipient{}, fmt.Errorf("--to %q is not <key> or <key>@<host>", to)
	}
	if key, err := operatorHandleSessionOf(local); err == nil {
		return operatorHandleRecipient{principal: local, sessionOf: key}, nil
	}
	what := "resolves to no live session on this host"
	r := operatorHandleRecipient{principal: local}
	if qualified {
		r.principal = to
		what = "names another host and cannot be verified here (cross-host handles are not verified yet)"
	}
	if !forceUnknown {
		return operatorHandleRecipient{}, fmt.Errorf("--to %q %s; check the key against `whoami` in that session, or pass --force-unknown to record the handle anyway", to, what)
	}
	r.note = fmt.Sprintf("recipient %s %s; recorded as given (--force-unknown)", to, what)
	return r, nil
}

// cloneHandleFields copies the part of a session record the operator command
// edits, so a judgement can be computed on a copy without touching the real
// record (the release helpers delete in place from shared backing arrays).
func cloneHandleFields(s *session.State) *session.State {
	c := &session.State{
		Holders:         slices.Clone(s.Holders),
		PendingHandles:  slices.Clone(s.PendingHandles),
		HandleRights:    maps.Clone(s.HandleRights),
		SpawnerReleased: s.SpawnerReleased,
	}
	if s.HandleProvenance != nil {
		c.HandleProvenance = maps.Clone(s.HandleProvenance)
	}
	return c
}

// operatorHandleEdit is what one session's edit does.
type operatorHandleEdit struct {
	granted bool     // the recipient was newly added
	removed []string // principals dropped (move only)
	spawner bool     // the legacy spawner key path was ended (move only)
}

func (e operatorHandleEdit) changed() bool { return e.granted || len(e.removed) > 0 || e.spawner }

// applyOperatorHandle edits cur in place: grant adds the recipient (pending,
// the named rights, provenance "operator"); move additionally releases every
// other holder and pending holder and ends the spawner's legacy key path
// (Q4). It is the one edit, used for the dry-run on a copy and for the write.
func applyOperatorHandle(cur *session.State, verb, to, rights string, at time.Time) operatorHandleEdit {
	var e operatorHandleEdit
	e.granted = cur.GrantHandleBy(to, rights, session.HandleOperator, at)
	if e.granted {
		if host, err := os.Hostname(); err == nil {
			p := cur.HandleProvenance[to]
			p.Host = host
			cur.HandleProvenance[to] = p
		}
	}
	if verb != operatorHandleMove {
		return e
	}
	for _, p := range slices.Concat(cur.Holders, cur.PendingHandles) {
		if p != to && cur.ReleaseHandle(p) {
			e.removed = append(e.removed, p)
		}
	}
	if !cur.SpawnerReleased {
		cur.SpawnerReleased = true
		e.spawner = true
	}
	return e
}

// operatorHandleJudgement is one session's plan before anything is written.
type operatorHandleJudgement struct {
	name    string
	st      *session.State
	skip    string // not a failure: why nothing is done for this session
	refusal string
	edit    operatorHandleEdit
	before  []string
}

func handleHolders(s *session.State) []string {
	out := slices.Clone(s.Holders)
	for _, p := range s.PendingHandles {
		out = append(out, p+" (pending)")
	}
	return out
}

// selectOperatorHandleSessions resolves the sessions to edit: the named ones
// (each judged on its own), or every live worktree session on the host.
func selectOperatorHandleSessions(p operatorHandleParams) []operatorHandleJudgement {
	if !p.AllOnHost {
		out := make([]operatorHandleJudgement, len(p.Target))
		for i, t := range p.Target {
			out[i].name = t
			st, err := findHandleTarget(t)
			if err == nil {
				err = guardHandleTarget(st, "edit handles of")
			}
			if err != nil {
				out[i].refusal = err.Error()
				continue
			}
			out[i].name, out[i].st = st.Key(), st
		}
		return out
	}
	all, err := session.ListAll(nil)
	if err != nil {
		return []operatorHandleJudgement{{name: "(all sessions)", refusal: fmt.Sprintf("listing sessions: %v", err)}}
	}
	var out []operatorHandleJudgement
	for i := range all {
		st := &all[i]
		j := operatorHandleJudgement{name: st.Key(), st: st}
		switch {
		case st.Kind == session.KindImplicit:
			j.skip = "a main-checkout (implicit) session holds no handles"
		case st.IsTombstone() || st.ResolveState() == session.StateAbandoned:
			j.skip = "closed"
		}
		out = append(out, j)
	}
	return out
}

// runOperatorHandle is `sc handle-grant` and `sc handle-move`.
func runOperatorHandle(verb string, p operatorHandleParams) error {
	cmd := "handle-" + verb
	if !operatorHandleIsTerminal() {
		return fmt.Errorf("%s: stdin and stdout must both be terminals; changing who holds a session is the operator's act and cannot be piped or driven by a program (an agent uses grant-session-handle)", cmd)
	}
	if p.AllOnHost == (len(p.Target) > 0) {
		return fmt.Errorf("%s: name at least one session, or pass --all-on-host (not both)", cmd)
	}
	rights := p.Rights
	if rights == "" {
		rights = session.DefaultHandleRights
	}
	wanted, err := session.ParseHandleRights(rights)
	if err != nil {
		return fmt.Errorf("%s: %w", cmd, err)
	}
	rights = wanted.String()
	recipient, err := resolveOperatorHandleRecipient(p.To, p.ForceUnknown)
	if err != nil {
		return fmt.Errorf("%s: %w", cmd, err)
	}

	at := operatorHandleNow()
	judged := selectOperatorHandleSessions(p)
	for i := range judged {
		j := &judged[i]
		if j.refusal != "" || j.skip != "" {
			continue
		}
		if recipient.sessionOf != "" && recipient.sessionOf == j.name {
			j.skip = "this is the recipient's own session"
			continue
		}
		j.before = handleHolders(j.st)
		j.edit = applyOperatorHandle(cloneHandleFields(j.st), verb, recipient.principal, rights, at)
	}

	errw := operatorHandleStderr()
	if recipient.note != "" {
		fmt.Fprintln(errw, cmd+": "+recipient.note)
	}
	pending := 0
	for _, j := range judged {
		if j.refusal == "" && j.skip == "" && j.edit.changed() {
			pending++
		}
	}
	if pending > 0 {
		describeOperatorHandlePlan(errw, verb, recipient.principal, rights, judged)
	}
	apply := pending > 0 && !p.DryRun
	if apply && !confirmOperatorHandle(errw, pending) {
		apply = false
		for i := range judged {
			if judged[i].refusal == "" && judged[i].skip == "" && judged[i].edit.changed() {
				judged[i].refusal = declinedAtThePrompt
			}
		}
	}

	var buf bytes.Buffer
	tw := tap.NewWriter(&buf)
	failed := 0
	for _, j := range judged {
		switch {
		case j.refusal != "":
			failed++
			tw.NotOk(j.name, map[string]string{"error": j.refusal})
		case j.skip != "":
			tw.Ok(j.name + ": skipped: " + j.skip)
		case !j.edit.changed():
			tw.Ok(j.name + ": nothing to change: " + recipient.principal + " already holds a handle (or is pending) and no one else would be removed")
		case !apply:
			tw.Ok(j.name + ": would " + describeOperatorHandleEdit(verb, recipient.principal, j.edit) + " (dry run)")
		default:
			if werr := writeOperatorHandleJournaled(j, verb, recipient.principal, rights, at); werr != nil {
				failed++
				tw.NotOk(j.name, map[string]string{"error": werr.Error()})
				continue
			}
			tw.Ok(j.name + ": " + describeOperatorHandleEdit(verb, recipient.principal, j.edit))
		}
	}
	tw.Plan()
	fmt.Fprint(operatorHandleStdout(), buf.String())
	if failed > 0 {
		return fmt.Errorf("%s: %d of %d session(s) not changed", cmd, failed, len(judged))
	}
	return nil
}

func writeOperatorHandle(st *session.State, verb, to, rights string, at time.Time) error {
	return updateLoadedSession(st, func(cur *session.State) bool {
		return applyOperatorHandle(cur, verb, to, rights, at).changed()
	})
}

// writeOperatorHandleJournaled is writeOperatorHandle with spinclass#480's
// effects: the grant is journaled first (fail-closed), each holder a move
// removes is journaled after (fail-open), and the recipient and the other
// holders are woken.
func writeOperatorHandleJournaled(j operatorHandleJudgement, verb, to, rights string, at time.Time) error {
	if j.edit.granted {
		if err := appendHandleJournal(handleJournalRecord{
			Time: at, Act: "grant", Child: j.name, Principal: to, Rights: rights,
			By: session.HandleOperator, Assurance: handleAssuranceTTY,
		}); err != nil {
			return fmt.Errorf("the grant could not be journaled, so it is not made: %w", err)
		}
	}
	if err := writeOperatorHandle(j.st, verb, to, rights, at); err != nil {
		if j.edit.granted {
			_ = appendHandleJournal(handleJournalRecord{
				Time: at, Act: "grant-not-written", Child: j.name, Principal: to, Rights: rights,
				By: session.HandleOperator, Assurance: handleAssuranceTTY, Note: err.Error(),
			})
		}
		return err
	}
	for _, p := range j.edit.removed {
		_ = appendHandleJournal(handleJournalRecord{
			Time: at, Act: "release", Child: j.name, Principal: p,
			By: session.HandleOperator, Assurance: handleAssuranceTTY, Note: "handle-move",
		})
	}
	if j.edit.granted {
		notifyHandleGrant(j.name, to, rights, session.HandleOperator, at, j.st.Holders)
	}
	return nil
}

func describeOperatorHandleEdit(verb, to string, e operatorHandleEdit) string {
	var parts []string
	if e.granted {
		parts = append(parts, "grant "+to+" a handle (effective now; "+to+" is woken)")
	}
	if len(e.removed) > 0 {
		parts = append(parts, "remove "+strings.Join(e.removed, ", "))
	}
	if e.spawner {
		parts = append(parts, "end the spawner's legacy key path")
	}
	return strings.Join(parts, "; ")
}

// describeOperatorHandlePlan prints what the batch would do, and lists every
// dangerous right the grant carries (Q2).
func describeOperatorHandlePlan(w io.Writer, verb, to, rights string, judged []operatorHandleJudgement) {
	fmt.Fprintf(w, "handle-%s: %s to %s with rights %s on these sessions:\n", verb, verb, to, rights)
	for _, j := range judged {
		if j.refusal != "" || j.skip != "" || !j.edit.changed() {
			continue
		}
		fmt.Fprintf(w, "  %s: now [%s] -> %s\n", j.name, strings.Join(j.before, ", "), describeOperatorHandleEdit(verb, to, j.edit))
	}
	for _, r := range strings.Split(rights, ",") {
		if why, dangerous := dangerousHandleRights[r]; dangerous {
			fmt.Fprintf(w, "  right %s: lets %s %s\n", r, to, why)
		}
	}
}

// confirmOperatorHandle asks once, on the terminal, for the whole batch. Only y
// or yes approves.
func confirmOperatorHandle(w io.Writer, n int) bool {
	fmt.Fprintf(w, "apply to %d session(s)? [y/N] ", n)
	line, _ := bufio.NewReader(operatorHandleConfirmIn()).ReadString('\n')
	if !strings.HasSuffix(line, "\n") {
		fmt.Fprintln(w)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// registerOperatorHandleCommands registers `sc handle-grant` and
// `sc handle-move`, CLI-only.
func registerOperatorHandleCommands(app *command.App) {
	for _, verb := range []string{operatorHandleGrant, operatorHandleMove} {
		short, long := "Add a handle on sessions for a principal, as the operator at a terminal (spinclass#478, FDR 0032)",
			"Adds the principal named by --to as a holder of each named session, keeping every existing holder."
		if verb == operatorHandleMove {
			short = "Move the handles of sessions to a principal, as the operator at a terminal (spinclass#478, FDR 0032)"
			long = "Adds the principal named by --to as a holder of each named session, then removes every other holder and pending holder " +
				"and ends the spawner's legacy key path, so the principal is the only authority."
		}
		app.AddCommand(&command.Command{
			Name:  "handle-" + verb,
			Title: "Change who holds session handles as the operator",
			Description: command.Description{
				Short: short,
				Long: long + " The operator is the authority: any live worktree session `sc list` shows can be named (or all of them with " +
					"--all-on-host), and no handle is needed. The grant is effective at once (spinclass#480): it is journaled, and the recipient and the other holders are woken. It carries " +
					"--rights (default observe,close; grant, merge, check, restart, instruct and cap must be named and are listed in the " +
					"prompt), and records GrantedBy \"operator\". --to is the principal key; it is checked against a live session on this " +
					"host and refused when it resolves to none, unless --force-unknown. A <key>@<host> naming another host is recorded as " +
					"given and unverified. Every session is judged first; --dry-run prints exactly what would change and exits; otherwise " +
					"one prompt (default no) covers the batch. It prints one TAP line per session, one failure does not stop the rest, and the " +
					"exit is nonzero if any failed. It refuses to run unless stdin and stdout are both terminals, so an agent cannot drive it.",
			},
			Params: []command.Param{
				{Name: "target", Type: command.String, Variadic: true, Description: "Sessions to change: <repo>/<branch> session keys or worktree directory names, exactly as `sc list` prints them (or use --all-on-host)", Completer: completeWorktreeTargets},
				{Name: "to", Type: command.String, Required: true, Description: "The recipient principal key (a chat JID localpart, as whoami shows it)"},
				{Name: "rights", Type: command.String, Description: "Comma-separated rights (observe, close, grant, instruct, cap, merge, check, restart). Defaults to observe,close"},
				{Name: "all-on-host", Type: command.Bool, Description: "Every live worktree session on this host instead of named sessions; the recipient's own session is skipped"},
				{Name: "dry-run", Type: command.Bool, Description: "Print exactly what would change and exit without writing"},
				{Name: "force-unknown", Type: command.Bool, Description: "Record the handle even when --to resolves to no live session on this host, or names another host"},
			},
			RunCLI: func(_ context.Context, args json.RawMessage) error {
				var p operatorHandleParams
				if err := json.Unmarshal(args, &p); err != nil {
					return fmt.Errorf("handle-%s: invalid arguments: %w", verb, err)
				}
				return runOperatorHandle(verb, p)
			},
		})
	}
}
