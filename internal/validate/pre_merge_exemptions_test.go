package validate

import (
	"testing"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

func TestCheckPreMergeExemptions(t *testing.T) {
	sf := sweatfile.Sweatfile{PreMergeExemptions: []sweatfile.PreMergeExemption{
		{Command: "true"},
		{Name: "lock-only", Command: "true"},
		{Name: "lock-only", Command: "false"},
		{Name: "stale"}, // name-only removal sentinel is valid
	}}
	issues := CheckPreMergeExemptions(sf)
	if len(issues) != 2 {
		t.Fatalf("expected missing-name error + duplicate warning, got %v", issues)
	}
	if issues[0].Severity != SeverityError || issues[1].Severity != SeverityWarning {
		t.Errorf("unexpected severities: %v", issues)
	}
}

// Exemptions only mean something while the attestation gate is live.
func TestCheckMergedWarnsOnExemptionsWithoutSkills(t *testing.T) {
	exemptions := []sweatfile.PreMergeExemption{{Name: "lock-only", Command: "true"}}
	found := func(issues []Issue) bool {
		for _, iss := range issues {
			if iss.Field == "pre-merge-exemptions" {
				return true
			}
		}
		return false
	}
	if !found(CheckMerged(sweatfile.Sweatfile{PreMergeExemptions: exemptions})) {
		t.Error("expected a dead-config warning when no pre-merge-skills are declared")
	}
	live := sweatfile.Sweatfile{
		PreMergeExemptions: exemptions,
		PreMergeSkills:     []sweatfile.PreMergeSkill{{Name: "review", Rationale: "r"}},
	}
	if found(CheckMerged(live)) {
		t.Error("no warning expected when the gate is live")
	}
}
