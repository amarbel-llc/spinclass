package close

import (
	"context"
	"fmt"
	"os"

	"code.linenisgreat.com/spinclass/internal/bravo/keyholdersweep"

	tap "code.linenisgreat.com/tap/go/pkgs/writer"
)

// stopKeyholder is the seam over the shared key holder teardown for tests.
var stopKeyholder = keyholdersweep.StopSession

// stopSessionKeyholder ends the session's key holder pair whatever the switch
// says now: a pair a session had before [keyholder] was turned off must not
// outlive the session. Like the agent unit, a pair that will not stop fails the
// close (its holder would keep a key for a session that is gone) unless force,
// which warns and carries on.
func stopSessionKeyholder(tw *tap.Writer, sessionKey, branch string, force bool) error {
	stopped, err := stopKeyholder(context.Background(), sessionKey)
	desc := "stop key holder " + branch
	switch {
	case err != nil && !force:
		if tw != nil {
			tw.NotOk(desc, map[string]string{"error": err.Error()})
		}
		return fmt.Errorf("stopping the key holder of %s: %w (%s, or use --force to close anyway)", branch, err, keyholdersweep.Recovery(sessionKey))
	case err != nil:
		// Closing anyway: the pair is left behind, so say how to stop it.
		msg := err.Error() + "; " + keyholdersweep.Recovery(sessionKey)
		if tw != nil {
			tw.NotOk(desc, map[string]string{"severity": "warn", "message": msg})
		} else {
			fmt.Fprintf(os.Stderr, "spinclass: %s failed: %s\n", desc, msg)
		}
	case stopped && tw != nil:
		tw.Ok(desc)
	}
	return nil
}
