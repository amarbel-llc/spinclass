package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/spawn"
)

var testEntry = []string{"clown", "--flag", "--", "claude"}

func fakeResolve(digest string, err error, calls *int) func(string) (spawn.ResolvedProfile, error) {
	return func(name string) (spawn.ResolvedProfile, error) {
		*calls++
		if err != nil {
			return spawn.ResolvedProfile{}, err
		}
		return spawn.ResolvedProfile{Name: name, Digest: digest}, nil
	}
}

func TestPlanRecordedProfileNoProfileUntouched(t *testing.T) {
	calls := 0
	plan, err := planRecordedProfile(session.State{}, fakeResolve("d", nil, &calls), testEntry, false)
	if err != nil || calls != 0 || !slices.Equal(plan.Entry, testEntry) {
		t.Fatalf("plan=%v err=%v calls=%d", plan, err, calls)
	}
}

func TestPlanRecordedProfileSplicesOnMatch(t *testing.T) {
	calls := 0
	st := session.State{Profile: "lean", ProfileDigest: "d1"}
	plan, err := planRecordedProfile(st, fakeResolve("d1", nil, &calls), testEntry, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"clown", "--flag", "--profile", "lean", "--", "claude"}
	if !slices.Equal(plan.Entry, want) || plan.RepinTo != "" || plan.Warning != "" {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestPlanRecordedProfileNoSeparatorRefuses(t *testing.T) {
	calls := 0
	st := session.State{Profile: "lean", ProfileDigest: "d1"}
	_, err := planRecordedProfile(st, fakeResolve("d1", nil, &calls), []string{"clown"}, false)
	if err == nil || !strings.Contains(err.Error(), "resume entry") {
		t.Fatalf("err=%v", err)
	}
}

func TestPlanRecordedProfileResolveFailureRefusesEvenWithRepin(t *testing.T) {
	calls := 0
	st := session.State{Profile: "lean", ProfileDigest: "d1"}
	boom := errors.New("registry skew")
	_, err := planRecordedProfile(st, fakeResolve("", boom, &calls), testEntry, true)
	if err == nil || !strings.Contains(err.Error(), "registry skew") {
		t.Fatalf("err=%v", err)
	}
}

func TestPlanRecordedProfileDigestMismatch(t *testing.T) {
	calls := 0
	st := session.State{Profile: "lean", ProfileDigest: "old"}
	_, err := planRecordedProfile(st, fakeResolve("new", nil, &calls), testEntry, false)
	if err == nil || !strings.Contains(err.Error(), "profile lean changed since this session was spawned (recorded old, now new); resume refused until an authority re-pins it") {
		t.Fatalf("err=%v", err)
	}
	plan, err := planRecordedProfile(st, fakeResolve("new", nil, &calls), testEntry, true)
	if err != nil || plan.RepinFrom != "old" || plan.RepinTo != "new" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestPlanRecordedProfileEmptyDigestWarns(t *testing.T) {
	calls := 0
	st := session.State{Profile: "lean"}
	plan, err := planRecordedProfile(st, fakeResolve("d", nil, &calls), testEntry, false)
	if err != nil || plan.Warning == "" || plan.RepinTo != "" || !slices.Contains(plan.Entry, "lean") {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}
