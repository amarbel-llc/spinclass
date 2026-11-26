package close

import (
	"context"
	"fmt"
	"os"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"

	tap "code.linenisgreat.com/tap/go/pkgs/writer"
)

// stopKeyholder is the seam over the key holder teardown for tests: it stops
// the session's pair (holder, then fibby, then their directories) and reports
// whether there was a pair to stop. A session that never had one costs a stat.
var stopKeyholder = func(ctx context.Context, sessionKey string) (bool, error) {
	base := keyholder.StateBase()
	l := &keyholder.Launcher{Base: base, Run: keyholder.SystemdRunner{}}
	if _, err := os.Lstat(keyholder.SessionDir(base, sessionKey)); err != nil {
		// No pair directory: the operator stopped the pair earlier (the key history
		// is kept, marked stopped), a start failed, or it is a driver, which never
		// had one. The session's key history, kind record and lock file end with the
		// session in EVERY one of those states: a later session with the same key
		// must not inherit a stale history and be told its first key "replaced" one.
		return false, l.ForgetSession(ctx, sessionKey)
	}
	return true, l.Stop(ctx, sessionKey)
}

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
		return fmt.Errorf("stopping the key holder of %s: %w (use --force to close anyway)", branch, err)
	case err != nil:
		if tw != nil {
			tw.NotOk(desc, map[string]string{"severity": "warn", "message": err.Error()})
		} else {
			fmt.Fprintf(os.Stderr, "spinclass: %s failed: %v\n", desc, err)
		}
	case stopped && tw != nil:
		tw.Ok(desc)
	}
	return nil
}
