package sweatfile_test

import (
	"testing"

	. "code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

func TestPreMergeSkillFields(t *testing.T) {
	sf := Sweatfile{
		PreMergeSkills: []PreMergeSkill{
			{Name: "eng:code-reviewer", Rationale: "Mandatory."},
		},
	}

	if len(sf.PreMergeSkills) != 1 || sf.PreMergeSkills[0].Name != "eng:code-reviewer" {
		t.Errorf("PreMergeSkills: got %v", sf.PreMergeSkills)
	}
	if sf.PreMergeSkills[0].Rationale != "Mandatory." {
		t.Errorf("PreMergeSkills[0].Rationale: got %q", sf.PreMergeSkills[0].Rationale)
	}
}

func TestActivePreMergeSkillsFiltersSentinels(t *testing.T) {
	sf := Sweatfile{
		PreMergeSkills: []PreMergeSkill{
			{Name: "eng:code-reviewer", Rationale: "Required."},
			{Name: "removed"}, // empty rationale = removal sentinel
		},
	}

	active := sf.ActivePreMergeSkills()
	if len(active) != 1 || active[0].Name != "eng:code-reviewer" {
		t.Errorf("ActivePreMergeSkills: got %v", active)
	}
}

func TestActivePreMergeSkillsEmpty(t *testing.T) {
	sf := Sweatfile{}
	if active := sf.ActivePreMergeSkills(); active != nil {
		t.Errorf("expected nil for empty PreMergeSkills, got %v", active)
	}
}

func TestMergePreMergeSkillsInherit(t *testing.T) {
	parent := Sweatfile{PreMergeSkills: []PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Mandatory."},
	}}
	child := Sweatfile{} // nil = inherit
	merged := parent.MergeWith(child)

	if len(merged.PreMergeSkills) != 1 || merged.PreMergeSkills[0].Name != "eng:code-reviewer" {
		t.Errorf("expected inherit, got %v", merged.PreMergeSkills)
	}
}

func TestMergePreMergeSkillsAppend(t *testing.T) {
	parent := Sweatfile{PreMergeSkills: []PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Mandatory."},
	}}
	child := Sweatfile{PreMergeSkills: []PreMergeSkill{
		{Name: "simplify", Rationale: "Prune abstractions."},
	}}
	merged := parent.MergeWith(child)

	if len(merged.PreMergeSkills) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(merged.PreMergeSkills), merged.PreMergeSkills)
	}
	if merged.PreMergeSkills[0].Name != "eng:code-reviewer" || merged.PreMergeSkills[1].Name != "simplify" {
		t.Errorf("unexpected order: %v", merged.PreMergeSkills)
	}
}

func TestMergePreMergeSkillsOverrideByName(t *testing.T) {
	parent := Sweatfile{PreMergeSkills: []PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Global default."},
		{Name: "simplify", Rationale: "Watch abstractions."},
	}}
	child := Sweatfile{PreMergeSkills: []PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Repo-specific reason."},
	}}
	merged := parent.MergeWith(child)

	if len(merged.PreMergeSkills) != 2 {
		t.Fatalf("expected 2, got %d", len(merged.PreMergeSkills))
	}
	// override in place at position 0
	if merged.PreMergeSkills[0].Name != "eng:code-reviewer" || merged.PreMergeSkills[0].Rationale != "Repo-specific reason." {
		t.Errorf("expected override in-place, got %+v", merged.PreMergeSkills[0])
	}
	if merged.PreMergeSkills[1].Name != "simplify" {
		t.Errorf("expected simplify preserved at index 1, got %+v", merged.PreMergeSkills[1])
	}
}

func TestMergePreMergeSkillsRemoveSentinel(t *testing.T) {
	parent := Sweatfile{PreMergeSkills: []PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Required everywhere."},
		{Name: "simplify", Rationale: "Watch abstractions."},
	}}
	child := Sweatfile{PreMergeSkills: []PreMergeSkill{
		{Name: "eng:code-reviewer"}, // empty rationale = removal sentinel
	}}
	merged := parent.MergeWith(child)

	// The sentinel is preserved in the slice (overrides in-place); ActivePreMergeSkills
	// filters it out so only simplify shows up as required.
	active := merged.ActivePreMergeSkills()
	if len(active) != 1 || active[0].Name != "simplify" {
		t.Errorf("expected only simplify in active list, got %v", active)
	}
}

func TestParsePreMergeSkillsFromTOML(t *testing.T) {
	input := []byte(`
[[pre-merge-skills]]
name      = "eng:code-reviewer"
rationale = "Mandatory."

[[pre-merge-skills]]
name      = "simplify"
rationale = "Prune."
`)
	doc, err := sweatfileio.Parse(input)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	sf := *doc.Data()

	if len(sf.PreMergeSkills) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(sf.PreMergeSkills))
	}
	if sf.PreMergeSkills[0].Name != "eng:code-reviewer" || sf.PreMergeSkills[0].Rationale != "Mandatory." {
		t.Errorf("PreMergeSkills[0]: got %+v", sf.PreMergeSkills[0])
	}
	if sf.PreMergeSkills[1].Name != "simplify" || sf.PreMergeSkills[1].Rationale != "Prune." {
		t.Errorf("PreMergeSkills[1]: got %+v", sf.PreMergeSkills[1])
	}
}

func TestParsePreMergeSkillsRemovalSentinel(t *testing.T) {
	input := []byte(`
[[pre-merge-skills]]
name = "to-remove"
`)
	doc, err := sweatfileio.Parse(input)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	sf := *doc.Data()

	if len(sf.PreMergeSkills) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(sf.PreMergeSkills))
	}
	if sf.PreMergeSkills[0].Rationale != "" {
		t.Errorf("expected empty rationale for sentinel, got %q", sf.PreMergeSkills[0].Rationale)
	}
}

func TestParsePreMergeSkillsNoUndecodedKeys(t *testing.T) {
	input := []byte(`
[[pre-merge-skills]]
name      = "eng:code-reviewer"
rationale = "Mandatory."
`)
	doc, err := sweatfileio.Parse(input)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	undecoded := doc.Undecoded()
	if len(undecoded) != 0 {
		t.Errorf("expected no undecoded keys, got %v", undecoded)
	}
}
