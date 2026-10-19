package main

import (
	"errors"
	"io"
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

func gateWith(noAttach, interactive bool, answer bool, asked *int) repinGate {
	return repinGate{NoAttach: noAttach, Interactive: interactive, Confirm: func(profile, from, to string) (bool, error) {
		*asked++
		if profile != "lean" || from != "old" || to != "new" {
			return false, errors.New("confirm was not shown the profile and both digests")
		}
		return answer, nil
	}}
}

func TestAuthorizeRepin(t *testing.T) {
	cases := []struct {
		name                          string
		noAttach, interactive, answer bool
		wantErr                       string
		wantAsked                     int
	}{
		{"no-attach refused", true, true, true, "--no-attach", 0},
		{"non-interactive refused", false, false, true, "interactive terminal", 0},
		{"declined refused", false, true, false, "declined", 1},
		{"accepted proceeds", false, true, true, "", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			asked := 0
			err := authorizeRepin("lean", "old", "new", gateWith(c.noAttach, c.interactive, c.answer, &asked))
			if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("err=%v, want %q", err, c.wantErr)
			}
			if asked != c.wantAsked {
				t.Fatalf("asked %d times, want %d", asked, c.wantAsked)
			}
		})
	}
}

// applyRecordedProfile is the code runResume runs between loading the sweatfile
// and attaching; these prove it plans, gates and persists.
func TestApplyRecordedProfilePersistsOnlyAnAuthorizedRepin(t *testing.T) {
	st := session.State{Profile: "lean", ProfileDigest: "old"}
	run := func(repin bool, gate repinGate) (entry []string, persisted []string, out string, err error) {
		calls := 0
		var stdout, stderr strings.Builder
		entry, err = applyRecordedProfile(st, fakeResolve("new", nil, &calls), testEntry, repin, gate,
			func(_ session.State, to string) error { persisted = append(persisted, to); return nil }, &stdout, &stderr)
		return entry, persisted, stdout.String(), err
	}
	asked := 0

	// Accepted: the new digest is persisted and the profile is spliced in.
	entry, persisted, out, err := run(true, gateWith(false, true, true, &asked))
	if err != nil || !slices.Equal(persisted, []string{"new"}) || !slices.Contains(entry, "lean") || !strings.Contains(out, "re-pinned from old to new") {
		t.Fatalf("accepted: entry=%v persisted=%v out=%q err=%v", entry, persisted, out, err)
	}

	// Each refusal persists nothing and yields no entry.
	for name, gate := range map[string]repinGate{
		"no-attach":       gateWith(true, true, true, &asked),
		"non-interactive": gateWith(false, false, true, &asked),
		"declined":        gateWith(false, true, false, &asked),
	} {
		entry, persisted, _, err := run(true, gate)
		if err == nil || persisted != nil || entry != nil {
			t.Errorf("%s: entry=%v persisted=%v err=%v", name, entry, persisted, err)
		}
	}

	// No flag: the mismatch refuses before the gate is consulted.
	asked = 0
	if _, persisted, _, err := run(false, gateWith(false, true, true, &asked)); err == nil || persisted != nil || asked != 0 {
		t.Fatalf("no --repin-profile: err=%v persisted=%v asked=%d", err, persisted, asked)
	}

	// A matching digest needs no gate at all, even with --no-attach.
	match := session.State{Profile: "lean", ProfileDigest: "new"}
	calls := 0
	got, err := applyRecordedProfile(match, fakeResolve("new", nil, &calls), testEntry, false, repinGate{NoAttach: true},
		func(session.State, string) error { t.Error("persisted without a re-pin"); return nil }, io.Discard, io.Discard)
	if err != nil || !slices.Contains(got, "lean") {
		t.Fatalf("matching digest: %v %v", got, err)
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
