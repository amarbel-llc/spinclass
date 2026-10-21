package clown

import (
	"testing"
	"time"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"

	"code.linenisgreat.com/spinclass/internal/testringmaster"
)

// registerPresence announces key to the in-process service. The descriptive
// fields (decoration, description) come from the environment the shared client
// was built under, so callers set CLOWN_GROUP_ID / CLOWN_GROUP_DESCRIPTION
// BEFORE the first jobwake call of the test (testringmaster.Start does none).
func registerPresence(t *testing.T, key string) {
	t.Helper()
	if err := jobwake.RegisterPresenceKey(key, time.Now()); err != nil {
		t.Fatalf("RegisterPresenceKey(%q): %v", key, err)
	}
}

func TestReadPresenceMapsServiceRecords(t *testing.T) {
	t.Setenv("CLOWN_GROUP_ID", "repo/feat")
	t.Setenv("CLOWN_GROUP_DESCRIPTION", "a feature")
	testringmaster.Start(t)
	registerPresence(t, "uuid-a")

	got := ReadPresence(time.Now())
	if len(got) != 1 {
		t.Fatalf("ReadPresence = %d records, want 1: %+v", len(got), got)
	}
	p := got[0]
	if p.SessionKey != "uuid-a" || p.ChannelID != jobwake.ChannelID("uuid-a") ||
		p.Decoration != "repo/feat" || p.Description != "a feature" || p.LastSeen == "" {
		t.Errorf("record not mapped 1:1 from the service: %+v", p)
	}
}

func TestReadPresenceNoServiceIsEmpty(t *testing.T) {
	// A runtime dir with no service listening: bare spinclass, no clown.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if got := ReadPresence(time.Now()); len(got) != 0 {
		t.Errorf("ReadPresence with no service = %+v, want empty", got)
	}
}

func TestPresenceByDecorationGroups(t *testing.T) {
	t.Setenv("CLOWN_GROUP_ID", "repo/feat")
	testringmaster.Start(t)
	registerPresence(t, "uuid-a")
	registerPresence(t, "uuid-b")

	byKey := PresenceByDecoration(time.Now())
	if len(byKey) != 1 {
		t.Errorf("groups = %d, want 1", len(byKey))
	}
	if len(byKey["repo/feat"]) != 2 {
		t.Errorf("repo/feat = %d clowns, want 2", len(byKey["repo/feat"]))
	}
}

func TestPresenceByDecorationSkipsUngrouped(t *testing.T) {
	t.Setenv("CLOWN_GROUP_ID", "")
	testringmaster.Start(t)
	registerPresence(t, "uuid-c")

	if byKey := PresenceByDecoration(time.Now()); len(byKey) != 0 {
		t.Errorf("groups = %+v, want none (ungrouped skipped)", byKey)
	}
}
