package main

import (
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
)

// With its own holder a spawn adds exactly one line; without one the output is
// unchanged, so a script reading it sees no difference.
func TestSpawnOutputSaysTheKeyHolderOnlyWhenTheWorkerHasOne(t *testing.T) {
	res := spawn.Result{SessionKey: "repo/w", WorktreePath: "/wt", MultiplexerID: "repo/w"}
	without := spawnResultText("driver", res)
	res.OwnKeyHolder = true
	with := spawnResultText("driver", res)
	if with != without+"\nkey_holder: own (see its key: sc session-key repo/w)" || strings.Contains(without, "key_holder") {
		t.Errorf("without:\n%s\nwith:\n%s", without, with)
	}
	pending := spawn.Pending{SessionKey: "repo/w", WorktreePath: "/wt"}
	if got := asyncSpawnResultText(pending, "driver", "job", time.Minute); strings.Contains(got, "key_holder") {
		t.Errorf("async without a holder: %s", got)
	}
	pending.OwnKeyHolder = true
	if got := asyncSpawnResultText(pending, "driver", "job", time.Minute); !strings.HasSuffix(got, "\nkey_holder: own (see its key: sc session-key repo/w)") {
		t.Errorf("async with a holder: %s", got)
	}
}
