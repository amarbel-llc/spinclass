package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// keyholderPairOut is the command's output stream; tests replace it.
var keyholderPairOut = func() io.Writer { return os.Stdout }

// keyholderPairRecipeEnv is the variable the live recipes (and only they) set to
// run start and stop without a terminal. It is NOT a boundary: any process of the
// account can set it, as any can run `systemd-run --user` itself. It keeps the
// plain call, a script, a driven agent and an accident out of a command that can
// replace a session's holder (A-H1); between unconfined sessions of one account
// there is no wall (docs/plans/2026-11-25-key-holder.md, "Stated limits").
const keyholderPairRecipeEnv = "SPINCLASS_KEYHOLDER_PAIR_RECIPE"

// keyholderPairRecipe reports whether a live recipe called the command; a var so
// a test can aim it.
var keyholderPairRecipe = func() bool { return os.Getenv(keyholderPairRecipeEnv) == "1" }

// registerKeyholderPairCommand registers the hidden `sc keyholder-pair`: start,
// stop or report a session's key holder pair by hand, for the live proof
// (`just debug-keyholder-pair-live`). It ignores the [keyholder] switch, which
// only governs what a session launch does on its own, and never touches a
// session's record or environment. Start and stop are the operator's act: they
// need stdin and stdout on a terminal, or the recipes' variable (status only
// reads and needs neither).
func registerKeyholderPairCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:   "keyholder-pair",
		Hidden: true,
		Title:  "Start, stop or report a session's key holder pair",
		Description: command.Description{
			Short: "Start, stop or report a session's key holder pair (debugging)",
			Long:  "action is start (idempotent; mints once), stop (holder, then fibby, then the directories) or status. The pair is keyed by the session key you give; no session need exist.",
		},
		Params: []command.Param{
			{Name: "action", Type: command.String, Required: true, Description: "start, stop or status"},
			{Name: "session", Type: command.String, Required: true, Description: "The session key the pair is named for"},
			{Name: "fibby", Type: command.String, Description: "Absolute path of the fibby holder build to run (both flags are required here: only a session launch reads [keyholder] fibby from the sweatfile)"},
			{Name: "piggy", Type: command.String, Description: "Absolute path of the piggy binary to run (both flags are required here: only a session launch reads [keyholder] piggy from the sweatfile)"},
			{Name: "systemd-run", Type: command.String, Description: "Absolute path of systemd-run, for a build that has no pin for it (a dev build); a production build uses its pin and never looks the name up on PATH"},
			{Name: "systemctl", Type: command.String, Description: "Absolute path of systemctl, for a build that has no pin for it (a dev build); a production build uses its pin and never looks the name up on PATH"},
		},
		RunCLI: runKeyholderPair,
	})
}

func runKeyholderPair(ctx context.Context, args json.RawMessage) error {
	var f struct {
		Action  string `json:"action"`
		Session string `json:"session"`
		Fibby   string `json:"fibby"`
		Piggy   string `json:"piggy"`
		Run     string `json:"systemd-run"`
		Ctl     string `json:"systemctl"`
	}
	if err := json.Unmarshal(args, &f); err != nil {
		return fmt.Errorf("keyholder-pair: %w", err)
	}
	if f.Session == "" {
		return errors.New("keyholder-pair: --session is required")
	}
	if (f.Action == "start" || f.Action == "stop") && !operatorHandleIsTerminal() && !keyholderPairRecipe() {
		return fmt.Errorf("keyholder-pair: %s replaces or ends a session's key holder: stdin and stdout must both be terminals (the live recipes set %s=1); `sc session-key` says why the key it prints is vouched only against confined sessions", f.Action, keyholderPairRecipeEnv)
	}
	launcher := func() (*keyholder.Launcher, error) { return keyholderLauncherFn(keyholder.Tools{}) }
	if f.Fibby != "" || f.Piggy != "" {
		launcher = func() (*keyholder.Launcher, error) {
			return launcherForToolsAndSystemd(f.Fibby, f.Piggy, f.Run, f.Ctl)
		}
	}
	l, err := launcher()
	if err != nil {
		return fmt.Errorf("keyholder-pair: %w", err)
	}
	out := keyholderPairOut()
	switch f.Action {
	case "start":
		p, err := l.Start(ctx, f.Session)
		if err != nil {
			return fmt.Errorf("keyholder-pair: %w", err)
		}
		fmt.Fprintf(out, "started %s\nfibby_unit %s\nagent_unit %s\nagent_sock %s\npublic_key %s\n", f.Session, p.FibbyUnit, p.AgentUnit, p.AgentSock, p.PublicKey)
	case "stop":
		if err := l.Stop(ctx, f.Session); err != nil {
			return fmt.Errorf("keyholder-pair: %w", err)
		}
		fmt.Fprintf(out, "stopped %s\n", f.Session)
	case "status":
		p, ok, err := l.Running(ctx, f.Session)
		if err != nil {
			return fmt.Errorf("keyholder-pair: %w", err)
		}
		if !ok {
			fmt.Fprintf(out, "not running %s\n", f.Session)
			return nil
		}
		// The pair record is a file a session of the account can write: its text
		// is printed only in its safe form.
		fmt.Fprintf(out, "running %s\nagent_sock %s\npublic_key %s\n", keyholder.SafeSession(f.Session), keyholder.SafeLine(p.AgentSock), keyholder.SafeLine(p.PublicKey))
	default:
		return fmt.Errorf("keyholder-pair: action %q is not start, stop or status", f.Action)
	}
	return nil
}
