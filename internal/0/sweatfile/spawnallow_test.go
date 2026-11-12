package sweatfile

import (
	"strings"
	"testing"
	"time"
)

// TEMPORARY (spinclass#384 stopgap): see spawnallow.go.
func TestParseSpawnAllow(t *testing.T) {
	two, zero := 2, 0
	cases := []struct {
		name    string
		in      SpawnAllow
		problem string // substring; "" means valid
		check   func(t *testing.T, e SpawnAllowEntry)
	}{
		{name: "principal with defaults", in: SpawnAllow{Principal: "abc"}, check: func(t *testing.T, e SpawnAllowEntry) {
			if e.Principal != "abc" || e.MaxWorkers != DefaultSpawnAllowMaxWorkers || !e.Expires.IsZero() {
				t.Errorf("entry = %+v", e)
			}
		}},
		{name: "qualified principal reduces to its key", in: SpawnAllow{Principal: "abc@flic.example"}, check: func(t *testing.T, e SpawnAllowEntry) {
			if e.Principal != "abc" || e.Host != "flic.example" {
				t.Errorf("entry = %+v", e)
			}
		}},
		{name: "session", in: SpawnAllow{Session: "circus/eager-holly", MaxWorkers: &two, Expires: "2026-11-19T00:00:00+01:00"}, check: func(t *testing.T, e SpawnAllowEntry) {
			want := time.Date(2026, 11, 18, 23, 0, 0, 0, time.UTC)
			if e.Session != "circus/eager-holly" || e.MaxWorkers != 2 || !e.Expires.Equal(want) {
				t.Errorf("entry = %+v, want expiry %v", e, want)
			}
		}},
		{name: "neither", in: SpawnAllow{}, problem: "neither"},
		{name: "both", in: SpawnAllow{Principal: "a", Session: "b/c"}, problem: "both"},
		{name: "principal with a slash", in: SpawnAllow{Principal: "a/b"}, problem: "not a principal key"},
		{name: "principal with two hosts", in: SpawnAllow{Principal: "a@b@c"}, problem: "<key>@<host>"},
		{name: "principal with no host", in: SpawnAllow{Principal: "a@"}, problem: "<key>@<host>"},
		{name: "session without a slash", in: SpawnAllow{Session: "circus"}, problem: "<repo>/<branch>"},
		{name: "zero cap", in: SpawnAllow{Principal: "a", MaxWorkers: &zero}, problem: "at least 1"},
		{name: "bad expiry", in: SpawnAllow{Principal: "a", Expires: "next week"}, problem: "RFC 3339"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := ParseSpawnAllow(tc.in)
			if tc.problem != "" {
				if !strings.Contains(e.Problem, tc.problem) {
					t.Fatalf("problem = %q, want it to contain %q", e.Problem, tc.problem)
				}
				return
			}
			if e.Problem != "" {
				t.Fatalf("unexpected problem %q", e.Problem)
			}
			tc.check(t, e)
		})
	}
}

func TestSpawnAllowEntryExpired(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	none := SpawnAllowEntry{}
	at := SpawnAllowEntry{Expires: now}
	later := SpawnAllowEntry{Expires: now.Add(time.Second)}
	if none.Expired(now) || !at.Expired(now) || later.Expired(now) {
		t.Errorf("none=%v at-instant=%v later=%v; want false, true, false", none.Expired(now), at.Expired(now), later.Expired(now))
	}
}
