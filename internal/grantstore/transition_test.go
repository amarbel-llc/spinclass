package grantstore

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/profilegrant"
)

func transitionRecord(t *testing.T, profile, from, to string) profilegrant.TransitionRecord {
	t.Helper()
	tr := profilegrant.Transition{
		Profile: profile, From: from, To: to,
		Expires: time.Date(2026, 10, 24, 16, 0, 0, 0, time.UTC), Nonce: "n-" + profile + "-" + to,
	}
	text := tr.CanonicalText()
	digest, err := profilegrant.DigestOf(text)
	if err != nil {
		t.Fatal(err)
	}
	return profilegrant.TransitionRecord{Digest: digest, Text: text, Diff: []string{"model: changed a -> b"}, Sig: []byte("sig")}
}

func TestTransitionStoreFindsOnlyTheExactChange(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := transitionRecord(t, "fable-design", "d-old", "d-new")
	for _, rec := range []profilegrant.TransitionRecord{
		want,
		transitionRecord(t, "fable-design", "d-old", "d-other"),
		transitionRecord(t, "fable-review", "d-old", "d-new"),
	} {
		if err := s.PutTransition(rec); err != nil {
			t.Fatalf("PutTransition: %v", err)
		}
	}
	got := s.FindTransitions("fable-design", "d-old", "d-new")
	if len(got) != 1 || got[0].Digest != want.Digest || !slices.Equal(got[0].Diff, want.Diff) {
		t.Fatalf("FindTransitions = %+v, want only %s", got, want.Digest)
	}
	if got := s.FindTransitions("fable-design", "d-new", "d-old"); len(got) != 0 {
		t.Errorf("the reverse change matched: %+v", got)
	}
}

func TestTransitionStorePutIsNoClobberAndIdempotent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec := transitionRecord(t, "p", "d-old", "d-new")
	if err := s.PutTransition(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTransition(rec); err != nil {
		t.Errorf("identical put: %v", err)
	}
	other := rec
	other.Sig = []byte("another signature")
	if err := s.PutTransition(other); err == nil {
		t.Error("a different record under an existing digest replaced it")
	}
}

func TestTransitionStoreRefusesAndIgnoresInconsistentRecords(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	bad := transitionRecord(t, "p", "d-old", "d-new")
	bad.Text = strings.Replace(bad.Text, "d-new", "d-evil", 1)
	if err := s.PutTransition(bad); err == nil {
		t.Error("a record whose digest does not name its text was stored")
	}
	// A file planted directly in the store is re-checked on read and ignored.
	good := transitionRecord(t, "p", "d-old", "d-new")
	if err := os.WriteFile(filepath.Join(dir, "transitions", good.Digest), []byte(`{"digest":"`+good.Digest+`","text":"tampered\n","sig":"c2ln"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.FindTransitions("p", "d-old", "d-new"); len(got) != 0 {
		t.Errorf("a tampered file was returned: %+v", got)
	}
}
