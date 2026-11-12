package session

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// #435: a handle says who granted it and when. Since #480 a grant is effective
// when recorded, so it is accepted at the moment it is granted.
func TestHandleProvenanceFollowsAGrantThroughRelease(t *testing.T) {
	var s State
	at := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	if !s.GrantHandleBy("p2", "observe,close", "p1", at) {
		t.Fatal("grant changed nothing")
	}
	got := s.ProvenanceText("p2")
	if want := "granted_by=p1 granted_at=2026-11-08T10:00:00Z accepted_at=2026-11-08T10:00:00Z"; got != want {
		t.Errorf("provenance = %q, want %q", got, want)
	}
	if s.AcceptHandle("p2") {
		t.Error("nothing is pending since #480, so accept must change nothing")
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

// A record written before #480 can still carry pending grants. Reading it
// promotes them to holders, accepted when they were granted (the operator's
// ruling: a grant is effective when recorded), and the record and its worktree
// copy then agree.
func TestPromotePendingHandlesMakesOldGrantsEffective(t *testing.T) {
	at := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	s := State{
		Holders:          []string{"spawner"},
		PendingHandles:   []string{"coord", "spawner"},
		HandleRights:     map[string]string{"coord": "observe,close,merge"},
		HandleProvenance: map[string]HandleProvenance{"coord": {GrantedBy: "operator", GrantedAt: &at}},
	}
	s.promotePendingHandles()
	if len(s.PendingHandles) != 0 {
		t.Errorf("PendingHandles = %v, want none", s.PendingHandles)
	}
	if want := []string{"spawner", "coord"}; !slices.Equal(s.Holders, want) {
		t.Errorf("Holders = %v, want %v (no duplicate)", s.Holders, want)
	}
	if got, want := s.ProvenanceText("coord"), "granted_by=operator granted_at=2026-11-12T13:00:00Z accepted_at=2026-11-12T13:00:00Z"; got != want {
		t.Errorf("provenance = %q, want %q", got, want)
	}
	if r, ok := s.RightsOf("coord"); !ok || !r.Has("merge") {
		t.Errorf("RightsOf(coord) = %v, %v; want its granted rights", r, ok)
	}
	// A pre-#435 pending grant has no provenance, and promotion invents none.
	old := State{PendingHandles: []string{"p3"}}
	old.promotePendingHandles()
	if !old.IsHolder("p3") || old.ProvenanceText("p3") != "provenance=unrecorded" {
		t.Errorf("pre-#435 pending grant: holder=%v provenance=%q", old.IsHolder("p3"), old.ProvenanceText("p3"))
	}
}

func TestGrantHandleWithoutAGranterStillStampsTheTime(t *testing.T) {
	var s State
	s.GrantHandle("p2", "")
	if got := s.ProvenanceText("p2"); !strings.HasPrefix(got, "granted_by=unknown granted_at=20") {
		t.Errorf("GrantHandle provenance = %q", got)
	}
}
