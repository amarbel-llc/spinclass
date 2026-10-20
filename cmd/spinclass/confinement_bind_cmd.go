package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/confinement"
	"code.linenisgreat.com/spinclass/internal/session"
)

// Seams for command tests.
var (
	confinementBindFind     = session.FindByTarget
	confinementBindRecord   = confinement.RecordPrincipalFromUnit
	confinementBindStateDir = confinement.DefaultStateDir
	confinementBindOut      = func() io.Writer { return os.Stdout }
)

// registerConfinementBindCommand registers `sc confinement-bind`: bind a
// running confined worker's principal to its session after the fact. A spawn
// whose hello is late used to return before binding, leaving a live unit with
// no frontend principal mapping (F15); this repairs one without a respawn.
// CLI-only: the mapping is what the grant walk trusts, so an agent must not be
// able to write one.
func registerConfinementBindCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "confinement-bind",
		Title: "Bind a confined worker's principal",
		Description: command.Description{
			Short: "Bind the principal in a running confined worker's unit record to its session (FDR 0035)",
			Long: "Reads the unit record clown wrote for the session's worktree and writes the frontend principal mapping from it. " +
				"The principal comes only from that record, never from the command line or the worktree. " +
				"Fails if the session has no unit record or the record names no principal. Idempotent.",
		},
		Params: []command.Param{
			{Name: "session", Type: command.String, Required: true, Description: "Session key (<repo>/<branch>) or worktree name of the confined worker"},
		},
		RunCLI: runConfinementBind,
	})
}

func runConfinementBind(_ context.Context, args json.RawMessage) error {
	var f struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(args, &f); err != nil {
		return fmt.Errorf("confinement-bind: %w", err)
	}
	if f.Session == "" {
		return errors.New("confinement-bind: --session is required")
	}
	st, err := confinementBindFind(f.Session)
	if err != nil {
		return fmt.Errorf("confinement-bind: %w", err)
	}
	if st.IsTombstone() {
		return fmt.Errorf("confinement-bind: session %s is closed", st.Key())
	}
	stateDir, err := confinementBindStateDir()
	if err != nil {
		return fmt.Errorf("confinement-bind: %w", err)
	}
	principal, err := confinementBindRecord(confinement.Host{}, stateDir, st.Key(), st.WorktreePath)
	if err != nil {
		return fmt.Errorf("confinement-bind: %w", err)
	}
	fmt.Fprintf(confinementBindOut(), "bound principal %s to session %s\n", principal, st.Key())
	return nil
}
