package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/session"
)

// With no [session-entry].resume the start entry (the shell) runs, which has no
// "--" to carry --profile. The shell fallback resumes, untouched, and names the
// profile; the definition check still applies.

var shellEntry = []string{"fish"}

var errBoom = errors.New("registry skew")

func TestShellFallbackWithARecordedProfileResumes(t *testing.T) {
	calls := 0
	st := session.State{Profile: "fable-design", ProfileDigest: "d1"}
	var stderr strings.Builder
	got, err := applyRecordedProfile(st, fakeResolve("d1", nil, &calls), shellEntry, false, true, repinGate{},
		func(session.State, string) error { t.Error("persisted without a re-pin"); return nil }, &stderr, &stderr)
	if err != nil {
		t.Fatalf("shell fallback refused: %v", err)
	}
	if !slices.Equal(got, shellEntry) {
		t.Errorf("entry = %v, want the shell untouched", got)
	}
	if calls != 1 {
		t.Errorf("profile resolved %d times, want 1: the definition is still checked", calls)
	}
	if !strings.Contains(stderr.String(), "fable-design") || !strings.Contains(stderr.String(), "d1") {
		t.Errorf("stderr = %q, want it to name the profile and its digest", stderr.String())
	}
}

func TestShellFallbackWithAChangedDigestRefuses(t *testing.T) {
	calls := 0
	st := session.State{Profile: "fable-design", ProfileDigest: "old"}
	_, err := applyRecordedProfile(st, fakeResolve("new", nil, &calls), shellEntry, false, true, repinGate{},
		func(session.State, string) error { t.Error("persisted"); return nil }, &strings.Builder{}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "changed since this session was spawned") {
		t.Fatalf("err = %v, want the changed-definition refusal", err)
	}
}

func TestShellFallbackWithAnUnresolvableProfileRefuses(t *testing.T) {
	calls := 0
	st := session.State{Profile: "fable-design", ProfileDigest: "d1"}
	if _, err := planRecordedProfile(st, fakeResolve("", errBoom, &calls), shellEntry, false, true); err == nil {
		t.Fatal("an unresolvable profile resumed through the shell fallback")
	}
}

// A configured resume entry without "--" still refuses: only the shell
// fallback is exempt.
func TestConfiguredResumeEntryWithoutSeparatorStillRefuses(t *testing.T) {
	calls := 0
	st := session.State{Profile: "fable-design", ProfileDigest: "d1"}
	if _, err := planRecordedProfile(st, fakeResolve("d1", nil, &calls), []string{"clown"}, false, false); err == nil {
		t.Fatal("a configured resume entry without -- was accepted")
	}
}

// Enforced profile rights refuse a configured harness entry that would mint a
// fresh principal; the shell fallback launches no harness, so it resumes.
func TestShellFallbackWithEnforcedProfileRightsResumes(t *testing.T) {
	got, _, err := applyRecordedPrincipal(shellEntry, recordedPrincipal, true, true)
	if err != nil || !slices.Equal(got, shellEntry) {
		t.Fatalf("got %v, err %v; want the shell untouched", got, err)
	}
	if _, _, err := applyRecordedPrincipal([]string{"clown"}, recordedPrincipal, true, false); err == nil {
		t.Error("a configured entry without {principal} must still refuse when enforced")
	}
}
