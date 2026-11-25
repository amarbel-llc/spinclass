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
	All    bool `json:"all"`
	DryRun bool `json:"dry-run"`
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

// runKeyholderStop is `sc keyholder-stop --all [--dry-run]`: stop every key
// holder pair this user has, the remedy when the switch is turned off (turning
// it off starts nothing new but leaves the pairs of existing sessions running
// until their sessions close). Both units of a pair are stopped and seen
// inactive before its directories are removed. It is the operator's act and
// refuses unless stdin and stdout are both terminals; --dry-run only lists, and
// needs no terminal. Each stop is journalled; a journal that cannot be written
// warns and the stop still happens, because stopping is the safe direction.
func runKeyholderStop(ctx context.Context, p keyholderStopParams) error {
	if !p.All {
		return errors.New("keyholder-stop: pass --all (it stops every key holder pair of this user)")
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
	orphans := 0
	for _, pr := range pairs {
		tag := "pair"
		if pr.Orphan {
			tag = "ORPHAN"
			orphans++
		}
		fmt.Fprintf(out, "%s %s\n", tag, keyholdersweep.Describe(pr))
	}
	if p.DryRun {
		fmt.Fprintf(out, "%d pair(s), %d of them orphaned (no live session; `sc clean` stops those); nothing was stopped (--dry-run)\n", len(pairs), orphans)
		return nil
	}
	var failed []string
	for _, pr := range pairs {
		rec := keyholderJournalRecord{At: operatorHandleNow(), Actor: "tty", Action: "stop", Hash: pr.Hash, Session: pr.Session, Result: "stopped"}
		serr := launcher.StopFound(ctx, pr.Found)
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
		return fmt.Errorf("keyholder-stop: %d of %d pair(s) did not stop (%v): a unit that will not stop leaves its directories in place", len(failed), len(pairs), failed)
	}
	return nil
}

// registerKeyholderStopCommand registers `sc keyholder-stop`.
func registerKeyholderStopCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "keyholder-stop",
		Title: "Stop every key holder pair",
		Description: command.Description{
			Short: "Stop every key holder pair of this user, as the operator at a terminal (the remedy when [keyholder] mode is turned off). CLI-only.",
			Long: "Turning [keyholder] mode off starts nothing for new sessions but leaves the pairs of existing sessions running until their sessions close; this stops them all: " +
				"both units of each pair are stopped and seen inactive before its directories are removed, and each stop is journalled to $XDG_STATE_HOME/spinclass/keyholder-journal.ndjson with actor tty. " +
				"--dry-run lists the pairs (and marks the ORPHANS, pairs whose session is gone, which `sc clean` stops) without stopping anything and needs no terminal; a real stop refuses unless stdin and stdout are both terminals. " +
				"It needs neither the trusted fibby and piggy paths nor the switch, so it works after the host's configuration is gone. A session whose pair was stopped keeps running without a holder; its next resume or restart starts a new pair and mints a new key if the switch is on.",
		},
		Params: []command.Param{
			{Name: "all", Type: command.Bool, Description: "Stop every pair (required)"},
			{Name: "dry-run", Short: 'n', Type: command.Bool, Description: "List the pairs and the orphans without stopping anything"},
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
