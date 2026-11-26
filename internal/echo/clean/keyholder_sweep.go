package clean

import (
	"context"
	"fmt"

	"code.linenisgreat.com/spinclass/internal/bravo/keyholdersweep"

	tap "code.linenisgreat.com/tap/go/pkgs/writer"
	"github.com/charmbracelet/log"
)

// The seams over the key holder sweep, for tests. A key holder pair whose session
// is gone (its record vanished, it was closed with --force, the directory was
// removed by hand and the units were left) has no session to close it: `sc clean`
// stops those, and `sc keyholder-stop --all --dry-run` lists them. A pair that is
// younger than keyholdersweep.OrphanMinAge with no record is a launch in flight
// and is left alone. Units this state root recorded nothing for belong to another
// state root (units are per user, the state root follows XDG_STATE_HOME): `sc
// clean` names them and never stops them.
var (
	findKeyholderOrphans = func() []keyholdersweep.Pair {
		pairs, err := keyholdersweep.Survey(context.Background())
		if err != nil {
			log.Warn("could not look for orphaned key holder pairs", "error", err)
			return nil
		}
		var orphans []keyholdersweep.Pair
		for _, p := range pairs {
			switch {
			case p.Orphan:
				orphans = append(orphans, p)
			case p.Unowned:
				log.Warn("key holder units this state root did not record: not mine, left running (sc keyholder-stop --all --including-unowned stops them)", "hash", p.Hash, "units", p.Units)
			}
		}
		return orphans
	}
	stopKeyholderOrphan = func(p keyholdersweep.Pair) error {
		return keyholdersweep.NewLauncher().StopFound(context.Background(), p.Found)
	}
)

// stopKeyholderOrphans stops each orphaned pair and reports it; a pair that
// will not stop is reported and left (its directories stay in place).
func stopKeyholderOrphans(tw *tap.Writer, orphans []keyholdersweep.Pair) {
	stopped := 0
	for _, p := range orphans {
		if err := stopKeyholderOrphan(p); err != nil {
			if tw != nil {
				tw.NotOk("stop orphaned key holder pair "+p.Hash, map[string]string{"error": err.Error()})
			} else {
				log.Error("failed to stop an orphaned key holder pair", "hash", p.Hash, "error", err)
			}
			continue
		}
		stopped++
	}
	if stopped > 0 {
		if tw != nil {
			tw.Ok(fmt.Sprintf("stopped %d orphaned key holder pair(s)", stopped))
		} else {
			log.Info("stopped orphaned key holder pairs", "count", stopped)
		}
	}
}
