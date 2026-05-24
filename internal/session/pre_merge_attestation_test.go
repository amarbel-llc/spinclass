package session

import (
	"testing"
	"time"
)

func TestPreMergeAttestationRoundTrip(t *testing.T) {
	s := setupTestSession(t, "att-branch")
	s.PreMergeAttestation = &PreMergeAttestation{
		RecordedAt: time.Now().UTC().Truncate(time.Second),
		Skills: []AttestedSkill{
			{Name: "eng:code-reviewer", Used: true, Reasoning: "Reviewed; no findings."},
			{Name: "simplify", Used: false, Reasoning: "Single-line bugfix."},
		},
	}

	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	loaded, err := Read(s.RepoPath, s.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PreMergeAttestation == nil {
		t.Fatal("expected PreMergeAttestation to round-trip, got nil")
	}
	if !loaded.PreMergeAttestation.RecordedAt.Equal(s.PreMergeAttestation.RecordedAt) {
		t.Errorf("RecordedAt = %v, want %v", loaded.PreMergeAttestation.RecordedAt, s.PreMergeAttestation.RecordedAt)
	}
	if got := loaded.PreMergeAttestation.Skills; len(got) != 2 {
		t.Fatalf("Skills: got %d entries, want 2", len(got))
	}
	if loaded.PreMergeAttestation.Skills[0].Name != "eng:code-reviewer" || !loaded.PreMergeAttestation.Skills[0].Used {
		t.Errorf("Skills[0]: got %+v", loaded.PreMergeAttestation.Skills[0])
	}
	if loaded.PreMergeAttestation.Skills[1].Name != "simplify" || loaded.PreMergeAttestation.Skills[1].Used {
		t.Errorf("Skills[1]: got %+v", loaded.PreMergeAttestation.Skills[1])
	}
}

func TestPreMergeAttestationOmittedWhenNil(t *testing.T) {
	s := setupTestSession(t, "no-att")
	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	loaded, err := Read(s.RepoPath, s.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PreMergeAttestation != nil {
		t.Errorf("expected nil PreMergeAttestation, got %+v", loaded.PreMergeAttestation)
	}
}
