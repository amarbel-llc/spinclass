package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	tap "code.linenisgreat.com/tap/go/pkgs/writer"
)

// `sc handle-rights` re-stamps the rights of an EXISTING holder, as the
// operator at a terminal. A spawn records its spawner's rights explicitly, as
// the vocabulary stood that day, and explicit sets stay (they never widen on
// their own), so every right added to the vocabulary later reaches an existing
// handle only through this verb. It only adds: the new set is the old set plus
// --add, so it can never narrow by accident. It shares handle-grant's guards:
// a terminal, every session judged first, one prompt for the batch, a TAP line
// per session, the journal before the write (fail-closed), and a wake to the
// holder and the other holders.

type operatorHandleRightsParams struct {
	Target    []string `json:"target"`
	Add       string   `json:"add"`
	Holder    string   `json:"holder"`
	Spawners  bool     `json:"spawners"`
	AllOnHost bool     `json:"all-on-host"`
	DryRun    bool     `json:"dry-run"`
}

// handleRightsPlan is one session's re-stamp before anything is written.
type handleRightsPlan struct {
	operatorHandleJudgement
	principal string
	before    string   // the rights held now, display order
	after     string   // before plus the added ones
	added     []string // the rights this re-stamp adds, display order
}

func (p handleRightsPlan) pending() bool {
	return p.refusal == "" && p.skip == "" && len(p.added) > 0
}

// handleRightsSubject picks the principal whose entry is re-stamped on st, or
// says why this session is skipped or refused.
func handleRightsSubject(st *session.State, p operatorHandleRightsParams) (principal, skip, refusal string) {
	if p.Spawners {
		spawner := st.SpawnedByPrincipal
		switch {
		case spawner == "":
			return "", "no recorded spawner", ""
		case st.SpawnerReleased:
			return "", "the spawner's handle was released", ""
		case !st.IsHolder(spawner):
			return "", "the recorded spawner " + spawner + " is not a holder", ""
		}
		principal = spawner
	} else {
		principal = p.Holder
		if !st.IsHolder(principal) {
			return "", "", principal + " is not a holder of " + st.Key() + ": this verb only widens an accepted handle; a new handle is handle-grant"
		}
	}
	if _, explicit := st.HandleRights[principal]; !explicit && principal == st.SpawnedByPrincipal {
		return "", "no explicit rights entry: the implicit spawner rule already gives " + principal + " every current right", ""
	}
	return principal, "", ""
}

// heldRights is principal's recorded rights on st: the explicit entry, or the
// default grant's for a holder that has none.
func heldRights(st *session.State, principal string) session.HandleRightSet {
	raw, explicit := st.HandleRights[principal]
	if !explicit {
		raw = session.DefaultHandleRights
	}
	held, _ := session.ParseHandleRights(raw)
	return held
}

// widenRights returns held plus add, and the names add contributed, both in
// display order.
func widenRights(held, add session.HandleRightSet) (after session.HandleRightSet, added []string) {
	after = session.HandleRightSet{}
	for _, n := range session.HandleRightNames {
		switch {
		case held[n]:
			after[n] = true
		case add[n]:
			after[n] = true
			added = append(added, n)
		}
	}
	return after, added
}

func runOperatorHandleRights(p operatorHandleRightsParams) error {
	const cmd = "handle-rights"
	if !operatorHandleIsTerminal() {
		return fmt.Errorf("%s: stdin and stdout must both be terminals; widening what a holder may do is the operator's act and cannot be piped or driven by a program", cmd)
	}
	if p.AllOnHost == (len(p.Target) > 0) {
		return fmt.Errorf("%s: name at least one session, or pass --all-on-host (not both)", cmd)
	}
	if (p.Holder != "") == p.Spawners {
		return fmt.Errorf("%s: name one holder with --holder, or pass --spawners for each session's recorded spawner (not both)", cmd)
	}
	add, err := session.ParseHandleRights(p.Add)
	if err != nil {
		return fmt.Errorf("%s: %w", cmd, err)
	}
	if len(add) == 0 {
		return fmt.Errorf("%s: --add is required: the rights to add (%s)", cmd, session.AllHandleRights)
	}

	at := operatorHandleNow()
	judged := selectOperatorHandleSessions(operatorHandleParams{Target: p.Target, AllOnHost: p.AllOnHost})
	plans := make([]handleRightsPlan, len(judged))
	pending := 0
	for i, j := range judged {
		plans[i].operatorHandleJudgement = j
		if j.refusal != "" || j.skip != "" {
			continue
		}
		principal, skip, refusal := handleRightsSubject(j.st, p)
		plans[i].principal, plans[i].skip, plans[i].refusal = principal, skip, refusal
		if principal == "" {
			continue
		}
		held := heldRights(j.st, principal)
		after, added := widenRights(held, add)
		plans[i].before, plans[i].after, plans[i].added = held.String(), after.String(), added
		if plans[i].pending() {
			pending++
		}
	}

	errw := operatorHandleStderr()
	if pending > 0 {
		describeHandleRightsPlan(errw, plans)
	}
	apply := pending > 0 && !p.DryRun
	if apply && !confirmOperatorHandle(errw, pending) {
		apply = false
		for i := range plans {
			if plans[i].pending() {
				plans[i].refusal = declinedAtThePrompt
			}
		}
	}

	var buf bytes.Buffer
	tw := tap.NewWriter(&buf)
	failed := 0
	for _, pl := range plans {
		what := "add " + strings.Join(pl.added, ",") + " to " + pl.principal
		switch {
		case pl.refusal != "":
			failed++
			tw.NotOk(pl.name, map[string]string{"error": pl.refusal})
		case pl.skip != "":
			tw.Ok(pl.name + ": skipped: " + pl.skip)
		case len(pl.added) == 0:
			tw.Ok(pl.name + ": nothing to add: " + pl.principal + " already holds " + add.String())
		case !apply:
			tw.Ok(pl.name + ": would " + what + " (dry run)")
		default:
			if werr := writeHandleRightsJournaled(pl, add, at); werr != nil {
				failed++
				tw.NotOk(pl.name, map[string]string{"error": werr.Error()})
				continue
			}
			tw.Ok(pl.name + ": " + what + " (now " + pl.after + "; the holders are woken)")
		}
	}
	tw.Plan()
	fmt.Fprint(operatorHandleStdout(), buf.String())
	if failed > 0 {
		return fmt.Errorf("%s: %d of %d session(s) not changed", cmd, failed, len(plans))
	}
	return nil
}

// writeHandleRightsJournaled journals the re-stamp, then writes it, re-reading
// the holder's entry under the record's lock so a concurrent change is widened
// rather than overwritten, then wakes the holders.
func writeHandleRightsJournaled(pl handleRightsPlan, add session.HandleRightSet, at time.Time) error {
	rec := handleJournalRecord{
		Time: at, Act: "rights-add", Child: pl.name, Principal: pl.principal, Rights: pl.after,
		By: session.HandleOperator, Assurance: handleAssuranceTTY,
		Note: "added " + strings.Join(pl.added, ",") + "; was " + pl.before,
	}
	if err := appendHandleJournal(rec); err != nil {
		return fmt.Errorf("the re-stamp could not be journaled, so it is not made: %w", err)
	}
	err := updateLoadedSession(pl.st, func(cur *session.State) bool {
		if !cur.IsHolder(pl.principal) {
			return false
		}
		after, added := widenRights(heldRights(cur, pl.principal), add)
		if len(added) == 0 {
			return false
		}
		if cur.HandleRights == nil {
			cur.HandleRights = map[string]string{}
		}
		cur.HandleRights[pl.principal] = after.String()
		return true
	})
	if err != nil {
		rec.Act, rec.Note = "rights-add-not-written", err.Error()
		_ = appendHandleJournal(rec)
		return err
	}
	notifyHandleRights(pl.name, pl.principal, strings.Join(pl.added, ","), pl.after, pl.st.Holders)
	return nil
}

// notifyHandleRights wakes the re-stamped holder and the other holders.
// Best-effort, like notifyHandleGrant: the journal is the durable record.
func notifyHandleRights(child, principal, added, after string, holders []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = notifyPrincipalFn(ctx, principal, session.HandleOperator, fmt.Sprintf(
		"handle rights widened: your handle on %s now also carries %s (the operator, at a terminal)\n\nchild: %s\nrights: %s",
		child, added, child, after))
	for _, h := range holders {
		if h == principal {
			continue
		}
		_ = notifyPrincipalFn(ctx, h, session.HandleOperator, fmt.Sprintf(
			"handle rights widened on %s: %s's handle now also carries %s (the operator, at a terminal)", child, principal, added))
	}
}

// describeHandleRightsPlan prints each session's before -> after and every
// dangerous right being added.
func describeHandleRightsPlan(w io.Writer, plans []handleRightsPlan) {
	fmt.Fprintln(w, "handle-rights: add rights to existing holders on these sessions:")
	seen := map[string]bool{}
	var dangerous []string
	for _, pl := range plans {
		if !pl.pending() {
			continue
		}
		fmt.Fprintf(w, "  %s: %s %s -> %s\n", pl.name, pl.principal, pl.before, pl.after)
		for _, r := range pl.added {
			if _, ok := dangerousHandleRights[r]; ok && !seen[r] {
				seen[r] = true
				dangerous = append(dangerous, r)
			}
		}
	}
	for _, r := range dangerous {
		fmt.Fprintf(w, "  right %s: lets the holder %s\n", r, dangerousHandleRights[r])
	}
}

// registerOperatorHandleRightsCommand registers `sc handle-rights`, CLI-only.
func registerOperatorHandleRightsCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "handle-rights",
		Title: "Add rights to existing session handles as the operator",
		Description: command.Description{
			Short: "Add rights to an existing holder's handle, as the operator at a terminal (FDR 0032)",
			Long: "Adds the rights named by --add to the handle of an existing holder on each named session (or every live worktree " +
				"session with --all-on-host): the holder named by --holder, or with --spawners each session's recorded spawner. " +
				"It only adds; nothing is removed, and no one is made a holder (that is handle-grant). A spawn records its spawner's " +
				"rights as the vocabulary stood that day, so a right added later reaches an existing handle only through this verb. " +
				"Skipped, not failed: closed and main-checkout sessions; with --spawners, a session with no recorded spawner, a released " +
				"spawner, or a spawner with no explicit rights entry (the implicit rule already gives every current right). Every session " +
				"is judged first; --dry-run prints each before -> after and exits; otherwise one prompt (default no) covers the batch and " +
				"lists every dangerous right being added. Each change is journaled before it is written (refused if it cannot be), and the " +
				"holder and the other holders are woken. It prints one TAP line per session, one failure does not stop the rest, and the " +
				"exit is nonzero if any failed. It refuses to run unless stdin and stdout are both terminals, so an agent cannot drive it.",
		},
		Params: []command.Param{
			{Name: "target", Type: command.String, Variadic: true, Description: "Sessions to change: <repo>/<branch> session keys or worktree directory names, exactly as `sc list` prints them (or use --all-on-host)", Completer: completeWorktreeTargets},
			{Name: "add", Type: command.String, Required: true, Description: "Comma-separated rights to add (" + session.AllHandleRights + ")"},
			{Name: "holder", Type: command.String, Description: "The holder whose handle gains the rights (a principal key, as list-handles shows it)"},
			{Name: "spawners", Type: command.Bool, Description: "Each session's recorded spawner, instead of one --holder"},
			{Name: "all-on-host", Type: command.Bool, Description: "Every live worktree session on this host instead of named sessions"},
			{Name: "dry-run", Type: command.Bool, Description: "Print each before -> after and exit without writing"},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p operatorHandleRightsParams
			if err := json.Unmarshal(args, &p); err != nil {
				return fmt.Errorf("handle-rights: invalid arguments: %w", err)
			}
			return runOperatorHandleRights(p)
		},
	})
}
