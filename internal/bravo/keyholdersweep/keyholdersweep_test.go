package keyholdersweep

import (
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

func TestClassifySeparatesLiveSessionsFromOrphans(t *testing.T) {
	live := map[string]bool{"s/live": true}
	old, young := time.Hour, time.Minute
	cases := []struct {
		name   string
		f      keyholder.Found
		orphan bool
		why    string
	}{
		{"a live session's pair", keyholder.Found{Hash: "a", Session: "s/live", Dir: "/d", Age: old}, false, "is live"},
		{"a closed session's pair", keyholder.Found{Hash: "b", Session: "s/closed", Dir: "/d", Age: old}, true, "closed or gone"},
		{"a directory with no record", keyholder.Found{Hash: "c", Dir: "/d", Age: old}, true, "no readable record"},
		{"units with no directory", keyholder.Found{Hash: "d", Units: []string{"u"}, Age: 0}, true, "no directory"},
		{"a launch in progress", keyholder.Found{Hash: "e", Dir: "/d", Age: young}, false, "launch may be in progress"},
		{"a young pair of a closed session", keyholder.Found{Hash: "f", Session: "s/closed", Dir: "/d", Age: young}, false, "launch may be in progress"},
	}
	var found []keyholder.Found
	for _, c := range cases {
		found = append(found, c.f)
	}
	got := Classify(found, live, OrphanMinAge)
	for i, c := range cases {
		if got[i].Orphan != c.orphan || !strings.Contains(got[i].Why, c.why) {
			t.Errorf("%s: orphan=%v why=%q, want orphan=%v why containing %q", c.name, got[i].Orphan, got[i].Why, c.orphan, c.why)
		}
	}
}

// A lock file with no pair is an orphan at any age: a launch in flight holds its
// lock, and the sweep never waits for or removes a held one.
func TestClassifyCallsALeftoverLockFileAnOrphanAtAnyAge(t *testing.T) {
	got := Classify([]keyholder.Found{{Hash: "0123456789abcdef", LockOnly: true, Age: time.Second}}, map[string]bool{}, time.Hour)
	if len(got) != 1 || !got[0].Orphan || !strings.Contains(got[0].Why, "lock file") {
		t.Fatalf("Classify = %+v, want an orphan lock file", got)
	}
}

// A kind record is a stray one only when no live session hashes to it: a driver
// has no pair, no history and no lock, and its kind file is the one trace of it.
func TestClassifyKeepsTheKindRecordOfALiveSessionAndSweepsAStrayOne(t *testing.T) {
	live := map[string]bool{"s/driver": true}
	found := []keyholder.Found{
		{Hash: keyholder.Hash("s/driver"), KindOnly: true, Age: time.Hour},
		{Hash: keyholder.Hash("s/gone"), KindOnly: true, Age: time.Second},
	}
	got := Classify(found, live, time.Hour)
	if got[0].Orphan || !strings.Contains(got[0].Why, "live session") {
		t.Errorf("a live driver's kind record: %+v, want it kept", got[0])
	}
	if !got[1].Orphan || !strings.Contains(got[1].Why, "stray kind record") {
		t.Errorf("a stray kind record: %+v, want an orphan", got[1])
	}
}

func TestDescribeNamesTheHashTheSessionAndTheState(t *testing.T) {
	line := Describe(Pair{Found: keyholder.Found{Hash: "0123456789abcdef", Running: true}, Why: "x"})
	for _, want := range []string{"0123456789abcdef", "session=unknown", "running"} {
		if !strings.Contains(line, want) {
			t.Errorf("%q lacks %q", line, want)
		}
	}
}
