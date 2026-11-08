package session

import (
	"strings"
	"testing"
	"time"
)

// #435: a handle says who granted it and when it was granted and accepted.
func TestHandleProvenanceFollowsAGrantThroughAcceptAndRelease(t *testing.T) {
	var s State
	at := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	if !s.GrantHandleBy("p2", "observe,close", "p1", at) {
		t.Fatal("grant changed nothing")
	}
	got := s.ProvenanceText("p2")
	if want := "granted_by=p1 granted_at=2026-11-08T10:00:00Z accepted_at=-"; got != want {
		t.Errorf("pending provenance = %q, want %q", got, want)
	}
	if !s.AcceptHandle("p2") {
		t.Fatal("accept changed nothing")
	}
	got = s.ProvenanceText("p2")
	if !strings.HasPrefix(got, "granted_by=p1 granted_at=2026-11-08T10:00:00Z accepted_at=20") {
		t.Errorf("accepted provenance = %q, want an accepted_at stamp", got)
	}
	if !s.ReleaseHandle("p2") {
		t.Fatal("release changed nothing")
	}
	if _, ok := s.HandleProvenance["p2"]; ok {
		t.Error("a released handle must not keep its provenance")
	}
}

func TestHandleProvenanceNamesTheSpawnAndMarksOldHandlesUnrecorded(t *testing.T) {
	var s State
	at := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	s.SeedSpawnerHandle("driver", at)
	if got, want := s.ProvenanceText("driver"), "spawned_at=2026-11-08T09:00:00Z"; got != want {
		t.Errorf("spawner provenance = %q, want %q", got, want)
	}
	s.SeedSpawnerHandle("", at) // no driver principal: nothing recorded
	if len(s.HandleProvenance) != 1 {
		t.Errorf("an empty principal must record nothing, got %v", s.HandleProvenance)
	}
	// A handle recorded before #435 has no entry, and accepting it does not
	// invent a granter.
	old := State{PendingHandles: []string{"p3"}}
	old.AcceptHandle("p3")
	if got := old.ProvenanceText("p3"); got != "provenance=unrecorded" {
		t.Errorf("pre-#435 handle = %q, want provenance=unrecorded", got)
	}
}

func TestGrantHandleWithoutAGranterStillStampsTheTime(t *testing.T) {
	var s State
	s.GrantHandle("p2", "")
	if got := s.ProvenanceText("p2"); !strings.HasPrefix(got, "granted_by=unknown granted_at=20") {
		t.Errorf("GrantHandle provenance = %q", got)
	}
}
