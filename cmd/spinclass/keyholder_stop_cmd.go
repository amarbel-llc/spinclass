package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/bravo/keyholdersweep"
)

// keyholderStopJournal is where each pair this command stops is recorded: an
// append-only NDJSON file beside the keyholder root. It is the operator's trail
// for the one act that ends sessions' keys, written with actor tty (as strong
// as isatty, not an authenticated act). A var so a test can aim it.
var keyholderStopJournal = func() string {
	return filepath.Join(filepath.Dir(keyholder.StateBase()), "keyholder-journal.ndjson")
}

type keyholderStopParams struct {
	All            bool `json:"all"`
	DryRun         bool `json:"dry-run"`
	IncludeUnowned bool `json:"including-unowned"`
}

type keyholderJournalRecord struct {
	At      time.Time `json:"at"`
	Actor   string    `json:"actor"`
	Action  string    `json:"action"`
	Hash    string    `json:"hash"`
	Session string    `json:"session,omitempty"`
	Result  string    `json:"result"`
	Error   string    `json:"error,omitempty"`
}

func appendKeyholderJournal(rec keyholderJournalRecord) error {
	path := keyholderStopJournal()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// runKeyholderStop is `sc keyholder-stop --all [--dry-run] [--including-unowned]`:
// stop every key holder pair THIS state root recorded, the remedy when the
// switch is turned off (turning it off starts nothing new but leaves the pairs
// of existing sessions running until their sessions close). Both units of a
// pair are stopped and seen inactive before its directories are removed. It is
// the operator's act and refuses unless stdin and stdout are both terminals;
// --dry-run only lists, and needs no terminal. Each stop is journalled; a journal
// that cannot be written warns and the stop still happens, because stopping is
// the safe direction.
//
// Units are per user but the state root follows XDG_STATE_HOME, so a shell with
// another state home sees every pair of the account as units with no directory.
// Those are listed as NOT MINE and left running; --including-unowned stops them
// too, after saying what it found. A pair whose session lives on keeps its key
// history, marked stopped, so the next start of that session is announced as a
// new key; the history of a pair whose session is gone goes with it.
func runKeyholderStop(ctx context.Context, p keyholderStopParams) error {
	if !p.All {
		return errors.New("keyholder-stop: pass --all (it stops every key holder pair this state root recorded)")
	}
	if !p.DryRun && !operatorHandleIsTerminal() {
		return errors.New("keyholder-stop: stdin and stdout must both be terminals; ending sessions' keys is the operator's act and cannot be piped or driven by a program (--dry-run lists without stopping)")
	}
	out, errOut := operatorHandleStdout(), operatorHandleStderr()
	launcher := keyholdersweep.NewLauncher()
	found, err := launcher.Find(ctx)
	if err != nil {
		return fmt.Errorf("keyholder-stop: %w", err)
	}
	live, err := keyholdersweep.LiveSessionKeys()
	if err != nil {
		return fmt.Errorf("keyholder-stop: %w", err)
	}
	pairs := keyholdersweep.Classify(found, live, keyholdersweep.OrphanMinAge)
	if len(pairs) == 0 {
		fmt.Fprintln(out, "no key holder pairs")
		return nil
	}
	orphans, unowned := 0, 0
	for _, pr := range pairs {
		tag := "pair"
		switch {
		case pr.Unowned:
			tag = "NOT MINE"
			unowned++
		case pr.Orphan:
			tag = "ORPHAN"
			orphans++
		}
		fmt.Fprintf(out, "%s %s\n", tag, keyholdersweep.Describe(pr))
	}
	if p.DryRun {
		fmt.Fprintf(out, "%d pair(s), %d of them orphaned (no live session; `sc clean` stops those), %d not this state root's (left running unless --including-unowned); nothing was stopped (--dry-run)\n", len(pairs), orphans, unowned)
		return nil
	}
	if unowned > 0 && !p.IncludeUnowned {
		fmt.Fprintf(errOut, "keyholder-stop: %d pair(s) are not recorded under this state root (%s) and are left running; pass --including-unowned to stop them too\n", unowned, keyholder.StateBase())
	}
	if unowned > 0 && p.IncludeUnowned {
		fmt.Fprintf(out, "will also stop %d pair(s) this state root did not record (--including-unowned), listed above as NOT MINE\n", unowned)
	}
	var failed []string
	stopping := 0
	for _, pr := range pairs {
		if pr.Unowned && !p.IncludeUnowned {
			continue
		}
		stopping++
		action := "stop"
		if pr.Unowned {
			action = "stop-unowned"
		}
		rec := keyholderJournalRecord{At: operatorHandleNow(), Actor: "tty", Action: action, Hash: pr.Hash, Session: pr.Session, Result: "stopped"}
		// A session that lives on keeps its key history, marked stopped: its next
		// start is a change and is announced. An orphan has nobody to tell.
		serr := launcher.StopFoundWith(ctx, pr.Found, keyholder.StopOptions{KeepHistory: !pr.Orphan && !pr.Unowned, IncludeUnowned: p.IncludeUnowned})
		if serr != nil {
			rec.Result, rec.Error = "failed", serr.Error()
			failed = append(failed, pr.Hash)
			fmt.Fprintf(errOut, "keyholder-stop: %s: %v\n", pr.Hash, serr)
		} else {
			fmt.Fprintf(out, "stopped %s\n", pr.Hash)
		}
		if jerr := appendKeyholderJournal(rec); jerr != nil {
			fmt.Fprintf(errOut, "keyholder-stop: warning: the stop of %s could not be journalled (%v); it was done all the same\n", pr.Hash, jerr)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("keyholder-stop: %d of %d pair(s) did not stop (%v): a unit that will not stop leaves its directories in place", len(failed), stopping, failed)
	}
	return nil
}

// registerKeyholderStopCommand registers `sc keyholder-stop`.
func registerKeyholderStopCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "keyholder-stop",
		Title: "Stop every key holder pair",
		Description: command.Description{
			Short: "Stop every key holder pair this state root recorded, as the operator at a terminal (the remedy when [keyholder] mode is turned off). CLI-only.",
			Long: "Turning [keyholder] mode off starts nothing for new sessions but leaves the pairs of existing sessions running until their sessions close; this stops them all: " +
				"both units of each pair are stopped and seen inactive before its directories are removed, and each stop is journalled to $XDG_STATE_HOME/spinclass/keyholder-journal.ndjson with actor tty. " +
				"A session that lives on keeps its key history, marked stopped, so its next resume or restart is announced as a new key. " +
				"Only pairs this state root recorded are stopped: units are per user but the state root follows XDG_STATE_HOME, so under another state home the account's pairs show as NOT MINE and are left running; --including-unowned stops them too, after listing them. " +
				"--dry-run lists the pairs (and marks the ORPHANS, pairs whose session is gone, which `sc clean` stops, and the NOT MINE ones) without stopping anything and needs no terminal; a real stop refuses unless stdin and stdout are both terminals. " +
				"It needs neither the trusted fibby and piggy paths nor the switch, so it works after the host's configuration is gone. A session whose pair was stopped keeps running without a holder; its next resume or restart starts a new pair and mints a new key if the switch is on.",
		},
		Params: []command.Param{
			{Name: "all", Type: command.Bool, Description: "Stop every pair this state root recorded (required)"},
			{Name: "dry-run", Short: 'n', Type: command.Bool, Description: "List the pairs, the orphans and the NOT MINE ones without stopping anything"},
			{Name: "including-unowned", Type: command.Bool, Description: "Also stop pairs whose units exist but this state root recorded nothing for (another XDG_STATE_HOME's pairs)"},
		},
		RunCLI: func(ctx context.Context, args json.RawMessage) error {
			var p keyholderStopParams
			if err := json.Unmarshal(args, &p); err != nil {
				return fmt.Errorf("keyholder-stop: invalid arguments: %w", err)
			}
			return runKeyholderStop(ctx, p)
		},
	})
}
