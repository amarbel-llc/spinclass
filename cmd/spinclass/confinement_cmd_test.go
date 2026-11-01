package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// The principal a confinement check judges a unit against must be the NAMED
// session's, not the caller's. CLOWN_SESSION_ID names the caller, so it is a
// default only when the caller IS the session being checked; for another
// session the recorded principal is used, with a warning that a default was
// chosen. Checking a child against the caller's principal made F1, F14 and
// F15 fail for a correctly confined unit (F14 counted the child's own bound
// credential as another session's).
func TestRunConfinementCheckDefaultsThePrincipalToTheNamedSession(t *testing.T) {
	t.Setenv("SPINCLASS_SESSION_ID", "driver/here")
	t.Setenv("CLOWN_SESSION_ID", "driver-principal")
	var gotSession, gotPrincipal string
	t.Cleanup(stubConfinementCheck(func(session, principal string) {
		gotSession, gotPrincipal = session, principal
	}))
	for name, tc := range map[string]struct {
		args          string
		wantPrincipal string
		wantWarning   bool
	}{
		"current session, no principal":  {`{}`, "driver-principal", false},
		"current session by name":        {`{"session":"driver/here"}`, "driver-principal", false},
		"another session, no principal":  {`{"session":"worker/other"}`, "", true},
		"another session with principal": {`{"session":"worker/other","principal":"p"}`, "p", false},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			confinementCheckErr = func() io.Writer { return &stderr }
			gotSession, gotPrincipal = "", ""
			if err := runConfinementCheck(context.Background(), json.RawMessage(tc.args)); err != nil {
				t.Fatalf("runConfinementCheck: %v", err)
			}
			if gotPrincipal != tc.wantPrincipal {
				t.Errorf("principal handed to InputsForDriver = %q, want %q (session %s)", gotPrincipal, tc.wantPrincipal, gotSession)
			}
			warned := strings.Contains(stderr.String(), "worker/other") && strings.Contains(stderr.String(), "recorded principal")
			if warned != tc.wantWarning {
				t.Errorf("warning = %v (stderr %q), want %v", warned, stderr.String(), tc.wantWarning)
			}
		})
	}
}

// stubConfinementCheck replaces the check's inputs and run seams with fakes
// that record what the command resolved and report every fact holding.
func stubConfinementCheck(record func(session, principal string)) (restore func()) {
	inputs, run, out, errOut := confinementInputsForDriver, confinementRun, confinementCheckOut, confinementCheckErr
	confinementInputsForDriver = func(session, principal string) (confinement.Inputs, error) {
		record(session, principal)
		return confinement.Inputs{}, nil
	}
	confinementRun = func(confinement.System, confinement.Inputs) confinement.Report {
		return confinement.Report{Facts: []confinement.Fact{{ID: "F1", Name: "stub", OK: true}}}
	}
	confinementCheckOut = func() io.Writer { return io.Discard }
	return func() {
		confinementInputsForDriver, confinementRun, confinementCheckOut, confinementCheckErr = inputs, run, out, errOut
	}
}
