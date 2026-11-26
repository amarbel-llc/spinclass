package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// `sc keyholder-switch [on|off|default]` is the operator's deploy-free switch for
// the key holder (issue #519). Today the switch could only be flipped by a
// circus merge and a deploy, in either direction, and "on in any layer wins"
// left no quick off. The switch file under the ACCOUNT's home (never one the
// caller's environment names) is the answer: `off` is a kill switch for new
// launches, `on` turns the key holder on, `default` leaves the layers to decide.
// The semantics live in applyKeyholderSwitchFile; this is the one place a person
// sets the file and reads the effective mode.
//
// Setting it needs a terminal, is journalled, and writes atomically. It stops no
// running pair (sc keyholder-stop --all does) and says so. A process of the same
// account can write the file as well as the operator (issue #515): the switch is
// the operator's setting, not a wall against his own account's sessions.

type keyholderSwitchParams struct {
	Value string `json:"value"`
}

// keyholderEffective is the effective mode as a launch from here would see it.
type keyholderEffective struct {
	On     bool
	Source string // "switch file", "layer" or "none"
	File   sweatfileio.KeyholderSwitchValue
	Path   string
	Err    error
}

// describeKeyholderSwitch resolves the switch the way a launch from the working
// directory does, for the bare command and for host-load. It never prints the
// one-line warning of a soft failure (a read-only report is not a launch).
func describeKeyholderSwitch() keyholderEffective {
	home, repo, err := keyholderCallerContext()
	if err != nil {
		return keyholderEffective{Source: "none", Err: err}
	}
	prev := keyholderWarnWriter
	keyholderWarnWriter = io.Discard
	defer func() { keyholderWarnWriter = prev }()
	sw, err := resolveKeyholderSwitch(home, repo)
	e := keyholderEffective{On: sw.On, Source: sw.Source(), File: sw.File, Path: sw.FilePath, Err: err}
	if err != nil {
		e.On, e.Source = false, "refused"
	}
	return e
}

// keyholderCallerContext is the home and the repo a launch from the working
// directory resolves the switch for.
func keyholderCallerContext() (home, repo string, err error) {
	home, herr := os.UserHomeDir()
	if herr != nil || home == "" {
		pw, perr := keyholderPasswdHomeFn()
		if perr != nil {
			return "", "", fmt.Errorf("neither $HOME nor the account database names a home: %v", perr)
		}
		home = pw
	}
	repo = keyholderCallerRepoFn()
	if repo == "" {
		repo = home
	}
	return home, repo, nil
}

// keyholderCallerRepoFn is the repo the working directory is in. A seam for tests.
var keyholderCallerRepoFn = driverRepoPath

// refuseOnWithoutPrograms is L-1 of the switch review: `on` is not written when
// it would refuse every worker launch, because no trusted [keyholder] layer
// supplies fibby and piggy. The layers are read the way a launch from here reads
// them (without the switch file: it is about to change); a layer that cannot be
// read is a refusal as well.
func refuseOnWithoutPrograms() error {
	home, repo, err := keyholderCallerContext()
	if err != nil {
		return fmt.Errorf("keyholder-switch: %w", err)
	}
	prev := keyholderWarnWriter
	keyholderWarnWriter = io.Discard
	defer func() { keyholderWarnWriter = prev }()
	sw, err := resolveKeyholderLayersAndPasswd(home, repo)
	if err != nil {
		return fmt.Errorf("keyholder-switch: not writing on: reading the [keyholder] layers fails, so every worker launch would be refused: %w", err)
	}
	var missing []string
	if sw.Fibby == "" {
		missing = append(missing, "fibby")
	}
	if sw.Piggy == "" {
		missing = append(missing, "piggy")
	}
	if len(missing) > 0 {
		return fmt.Errorf("keyholder-switch: not writing on: no trusted [keyholder] layer sets %s, so every worker launch would be refused. The host's configuration writes `fibby = \"/nix/store/.../bin/fibby\"` and `piggy = \"/nix/store/.../bin/piggy\"` under [keyholder] in the global sweatfile (~/.config/spinclass/sweatfile) or a layer above the repo; nothing was changed", strings.Join(missing, " and "))
	}
	if sw.ToolConflict != nil {
		return fmt.Errorf("keyholder-switch: not writing on: %w", sw.ToolConflict)
	}
	return nil
}

// String is the one line `sc host-load` shows (and the bare command prints
// first): the effective mode and where it came from.
func (e keyholderEffective) String() string {
	if e.Err != nil {
		return fmt.Sprintf("key holder: launches are REFUSED: %s", keyholder.SafeLine(e.Err.Error()))
	}
	mode := "off"
	if e.On {
		mode = "on"
	}
	line := fmt.Sprintf("key holder: %s (source: %s)", mode, e.Source)
	if e.File != "" {
		line += fmt.Sprintf("; switch file %s says %s", e.Path, e.File)
	}
	return line
}

// worthShowing says whether host-load has anything to say: nothing on a host
// that never used the key holder or its switch file, so its output is as before.
func (e keyholderEffective) worthShowing() bool {
	return e.Err != nil || e.On || e.File != ""
}

func runKeyholderSwitch(_ context.Context, p keyholderSwitchParams) error {
	out, errOut := operatorHandleStdout(), operatorHandleStderr()
	if p.Value == "" {
		e := describeKeyholderSwitch()
		fmt.Fprintln(out, e.String())
		if e.Path == "" {
			if pw, err := keyholderPasswdHomeFn(); err == nil {
				fmt.Fprintf(out, "switch file: %s (absent: the layers decide)\n", sweatfileio.KeyholderSwitchPath(pw))
			}
		}
		return nil
	}
	want, err := sweatfileio.ParseKeyholderSwitchValue(p.Value)
	if err != nil {
		return fmt.Errorf("keyholder-switch: %w (give on, off or default; no argument prints the effective mode)", err)
	}
	if !operatorHandleIsTerminal() {
		return errors.New("keyholder-switch: stdin and stdout must both be terminals; flipping the key holder is the operator's act and cannot be piped or driven by a program (no argument prints the effective mode and needs no terminal)")
	}
	pw, err := keyholderPasswdHomeFn()
	if err != nil {
		return fmt.Errorf("keyholder-switch: resolving the account's home: %w (the switch file lives under it, never under $HOME alone)", err)
	}
	if want == sweatfileio.KeyholderSwitchOn {
		// L-1: do not write on when it would refuse every worker launch.
		if err := refuseOnWithoutPrograms(); err != nil {
			return err
		}
	}
	old, present, rerr := sweatfileio.ReadKeyholderSwitch(pw)
	oldWord := string(old)
	switch {
	case rerr != nil:
		oldWord = "invalid"
	case !present:
		oldWord = "absent"
	}
	// L-6: on leaves a marker under EVERY state root the evidence gate reads (the
	// one the operator's environment names and the account's own, so neither a
	// different XDG_STATE_HOME nor a failing account lookup can hide it): a later
	// failure to look up the account's home refuses launches instead of warning and
	// proceeding on the operator's agent. It is written BEFORE the file, so there is
	// never an on file without it; off and default take it away afterwards.
	roots := keyholderEvidenceRoots()
	var wrote []string
	if want == sweatfileio.KeyholderSwitchOn {
		for _, root := range roots {
			had := keyholder.HasSwitchMarker(root)
			if err := keyholder.WriteSwitchMarker(root); err != nil {
				removeSwitchMarkers(wrote)
				return fmt.Errorf("keyholder-switch: nothing was changed: leaving the evidence marker under %s: %w", root, err)
			}
			if !had {
				wrote = append(wrote, root)
			}
		}
	}
	if err := sweatfileio.WriteKeyholderSwitch(pw, want); err != nil {
		removeSwitchMarkers(wrote)
		return fmt.Errorf("keyholder-switch: %w", err)
	}
	if want != sweatfileio.KeyholderSwitchOn {
		for _, root := range roots {
			if err := keyholder.RemoveSwitchMarker(root); err != nil {
				fmt.Fprintf(errOut, "keyholder-switch: warning: %v (the switch file was changed all the same)\n", err)
			}
		}
	}
	rec := keyholderJournalRecord{
		At: operatorHandleNow(), Actor: "tty", Action: "switch", Result: "set",
		Old: oldWord, New: string(want), Uid: os.Getuid(), Tty: ttyName(),
	}
	if jerr := appendKeyholderJournal(rec); jerr != nil {
		fmt.Fprintf(errOut, "keyholder-switch: warning: the change could not be journalled (%v); it was made all the same\n", jerr)
	}
	fmt.Fprintf(out, "key holder switch file %s: %s -> %s\n", sweatfileio.KeyholderSwitchPath(pw), oldWord, want)
	fmt.Fprintln(out, "this decides NEW launches only: it stops no running pair (`sc keyholder-stop --all` does), and sessions already running keep what they have")
	fmt.Fprintln(out, "limit (issue #515): any process of this account can write this file, as you can; it is the operator's setting, not a wall against the account's own sessions")
	if want == sweatfileio.KeyholderSwitchOn {
		var paths []string
		for _, root := range roots {
			paths = append(paths, keyholder.SwitchMarkerPath(root))
		}
		fmt.Fprintf(out, "evidence marker left (%s): a failure to look up the account's home now refuses launches instead of warning and proceeding on the operator's agent; `off` or `default` removes it\n", strings.Join(paths, ", "))
		if e := describeKeyholderSwitch(); e.Err != nil {
			fmt.Fprintf(errOut, "keyholder-switch: warning: %s\n", e)
		}
	}
	return nil
}

// removeSwitchMarkers takes back the markers this run left when the change did
// not go through.
func removeSwitchMarkers(roots []string) {
	for _, root := range roots {
		_ = keyholder.RemoveSwitchMarker(root)
	}
}

// ttyName is the terminal stdin is, best effort, for the journal.
func ttyName() string {
	p, err := os.Readlink("/proc/self/fd/0")
	if err != nil {
		return ""
	}
	return keyholder.SafeLine(strings.TrimSpace(p))
}

// registerKeyholderSwitchCommand registers `sc keyholder-switch`.
func registerKeyholderSwitchCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "keyholder-switch",
		Title: "Set or show the key holder's deploy-free switch",
		Description: command.Description{
			Short: "Set the key holder switch file on, off or default as the operator at a terminal, or (no argument) print the effective mode and where it came from. CLI-only.",
			Long: "The switch file lives under the account's home (~/.config/spinclass/keyholder-switch, resolved through the account database, never the environment) and holds one word. " +
				"off is a kill switch: the key holder is off for NEW launches whatever any sweatfile layer says (the one exception to \"on in any trusted layer wins\"). " +
				"on turns the key holder on; fibby and piggy still come from the trusted [keyholder] layers, and with none supplying them every launch is refused. " +
				"default (or no file) leaves the layers to decide. A file that exists and is not one of the three words, or cannot be read, refuses every launch and names the way out. " +
				"Setting it needs a terminal, is journalled to $XDG_STATE_HOME/spinclass/keyholder-journal.ndjson and writes atomically. " +
				"It stops no running pair: sc keyholder-stop --all does. " +
				"Limit (issue #515): any process of the same account can write the file; it is the operator's setting, not a wall against the account's own sessions. " +
				"With no argument it prints the effective mode and its source (switch file, layer or none) and needs no terminal; sc validate and sc host-load show the same.",
		},
		Examples: []command.Example{
			{Description: "Turn the key holder off for new launches (the kill switch), without a deploy", Command: "sc keyholder-switch off"},
			{Description: "Let the sweatfile layers decide again", Command: "sc keyholder-switch default"},
			{Description: "Show the effective mode and where it came from", Command: "sc keyholder-switch"},
		},
		Params: []command.Param{
			{Name: "value", Type: command.String, Description: "on, off or default; omit to print the effective mode and its source"},
		},
		RunCLI: func(ctx context.Context, args json.RawMessage) error {
			var p keyholderSwitchParams
			if len(args) > 0 {
				if err := json.Unmarshal(args, &p); err != nil {
					return fmt.Errorf("keyholder-switch: invalid arguments: %w", err)
				}
			}
			return runKeyholderSwitch(ctx, p)
		},
	})
}
