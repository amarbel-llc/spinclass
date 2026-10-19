package main

import (
	"slices"
	"strings"
	"testing"
)

const recordedPrincipal = "1d3a5c7e-9b0f-4d2a-8e6c-0a1b2c3d4e5f"

// The FDR 0034 item 17 fix: the recorded principal reaches clown as the
// --resume id, so the resumed worker keeps its principal.
func TestResumeEntrySubstitutesRecordedPrincipal(t *testing.T) {
	entry := []string{"direnv", "exec", "/wt", "clown", "--", "--resume", "{principal}"}
	got, warning, err := applyRecordedPrincipal(entry, recordedPrincipal, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"direnv", "exec", "/wt", "clown", "--", "--resume", recordedPrincipal}
	if !slices.Equal(got, want) {
		t.Errorf("entry = %v, want %v", got, want)
	}
	if warning != "" {
		t.Errorf("unexpected warning %q", warning)
	}
	if entry[6] != "{principal}" {
		t.Error("the configured entry was mutated in place")
	}
}

func TestResumeEntrySubstitutesInsideAnArgument(t *testing.T) {
	got, _, err := applyRecordedPrincipal([]string{"clown", "resume", "clown://claude/{principal}"}, recordedPrincipal, false)
	if err != nil {
		t.Fatal(err)
	}
	if got[2] != "clown://claude/"+recordedPrincipal {
		t.Errorf("got %q", got[2])
	}
}

func TestResumeWithoutPlaceholderWarnsUnenforcedAndRefusesEnforced(t *testing.T) {
	entry := []string{"clown"}
	got, warning, err := applyRecordedPrincipal(entry, recordedPrincipal, false)
	if err != nil || !slices.Equal(got, entry) || !strings.Contains(warning, "fresh principal") {
		t.Errorf("unenforced: got %v, warning %q, err %v", got, warning, err)
	}
	if _, _, err := applyRecordedPrincipal(entry, recordedPrincipal, true); err == nil || !strings.Contains(err.Error(), "enforced") {
		t.Errorf("enforced: err = %v, want a refusal", err)
	}
}

func TestResumeWithoutPrincipalOrPlaceholderIsUnchanged(t *testing.T) {
	got, warning, err := applyRecordedPrincipal([]string{"zsh"}, "", true)
	if err != nil || warning != "" || !slices.Equal(got, []string{"zsh"}) {
		t.Errorf("got %v, %q, %v", got, warning, err)
	}
}

func TestResumePlaceholderRefusesMissingOrMalformedPrincipal(t *testing.T) {
	entry := []string{"clown", "--", "--resume", "{principal}"}
	if _, _, err := applyRecordedPrincipal(entry, "", false); err == nil {
		t.Error("substituted an empty principal")
	}
	if _, _, err := applyRecordedPrincipal(entry, "not-a-uuid; rm -rf", false); err == nil {
		t.Error("substituted a non-UUID principal")
	}
}
