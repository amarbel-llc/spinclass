package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// confinementCheckOut is the command's output stream (a seam for tests).
var confinementCheckOut = func() io.Writer { return os.Stdout }

// registerConfinementCheckCommand registers `sc confinement-check`: the FDR
// 0035 table for one session, one TAP line per fact. CLI-only (RunCLI, no
// Run): it is the operator's diagnostic, and the authorizing check runs inside
// spawn, not here.
func registerConfinementCheckCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "confinement-check",
		Title: "Check session confinement",
		Description: command.Description{
			Short: "Print which session-confinement facts (FDR 0035) hold for a session's agent unit",
			Long: "Probes, from this process, every fact FDR 0035 requires before a profile grant may be treated as card-rooted " +
				"(FDR 0034), and prints one TAP line per fact with the reason it holds or fails. Exits nonzero unless every fact holds. " +
				"The session defaults to the current one; the principal to CLOWN_SESSION_ID, else the session's recorded principal. " +
				"Run it from outside the session's agent unit (the frontend): run from inside, the unit's own view makes several facts unprovable.",
		},
		Params: []command.Param{
			{Name: "session", Type: command.String, Description: "Session key (<repo>/<branch>); defaults to the current session"},
			{Name: "principal", Type: command.String, Description: "Principal the unit must carry; defaults to CLOWN_SESSION_ID, else the recorded one"},
		},
		RunCLI: runConfinementCheck,
	})
}

func runConfinementCheck(_ context.Context, args json.RawMessage) error {
	var f struct {
		Session   string `json:"session"`
		Principal string `json:"principal"`
	}
	if err := json.Unmarshal(args, &f); err != nil {
		return fmt.Errorf("confinement-check: %w", err)
	}
	if f.Session == "" {
		f.Session = bestEffortSessionKey()
	}
	if f.Principal == "" {
		f.Principal = os.Getenv("CLOWN_SESSION_ID")
	}
	in, err := confinement.InputsForDriver(f.Session, f.Principal)
	if err != nil {
		return fmt.Errorf("confinement-check: %w", err)
	}
	report := confinement.Run(confinement.Host{}, in)
	writeConfinementTAP(confinementCheckOut(), f.Session, report)
	if !report.OK() {
		return fmt.Errorf("confinement-check: session confinement does not hold for %s", f.Session)
	}
	return nil
}

func writeConfinementTAP(w io.Writer, session string, r confinement.Report) {
	fmt.Fprintln(w, "TAP version 14")
	fmt.Fprintf(w, "# session confinement (FDR 0035) for %s\n", session)
	fmt.Fprintf(w, "1..%d\n", len(r.Facts))
	for i, fact := range r.Facts {
		status := "ok"
		if !fact.OK {
			status = "not ok"
		}
		fmt.Fprintf(w, "%s %d - %s %s\n", status, i+1, fact.ID, fact.Name)
		if fact.Detail != "" {
			for _, line := range strings.Split(fact.Detail, "; ") {
				fmt.Fprintf(w, "# %s\n", line)
			}
		}
	}
}
