package sweatfile_test

import (
	"testing"

	. "code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

func TestParsePreMergeExemptionsFromTOML(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte(`
[[pre-merge-exemptions]]
name    = "lock-only"
command = "bash check.bash \"$SPINCLASS_MERGE_BASE\" \"$SPINCLASS_LANDING_SHA\""
`))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if undecoded := doc.Undecoded(); len(undecoded) != 0 {
		t.Errorf("expected no undecoded keys, got %v", undecoded)
	}
	sf := *doc.Data()
	if len(sf.PreMergeExemptions) != 1 || sf.PreMergeExemptions[0].Name != "lock-only" {
		t.Fatalf("PreMergeExemptions: got %+v", sf.PreMergeExemptions)
	}
	if got := sf.PreMergeExemptions[0].Command; got != `bash check.bash "$SPINCLASS_MERGE_BASE" "$SPINCLASS_LANDING_SHA"` {
		t.Errorf("Command: got %q", got)
	}
}

// Dedup-by-name like [[pre-merge-skills]]: a child overrides in place, a new
// name appends, and a name-only child entry removes the inherited predicate.
func TestMergePreMergeExemptions(t *testing.T) {
	parent := Sweatfile{PreMergeExemptions: []PreMergeExemption{
		{Name: "lock-only", Command: "parent"},
		{Name: "docs-only", Command: "docs"},
	}}
	child := Sweatfile{PreMergeExemptions: []PreMergeExemption{
		{Name: "lock-only", Command: "child"},
		{Name: "docs-only"},
		{Name: "extra", Command: "extra"},
	}}
	active := parent.MergeWith(child).ActivePreMergeExemptions()
	if len(active) != 2 {
		t.Fatalf("expected 2 active, got %+v", active)
	}
	if active[0].Name != "lock-only" || active[0].Command != "child" {
		t.Errorf("expected in-place override, got %+v", active[0])
	}
	if active[1].Name != "extra" {
		t.Errorf("expected extra appended, got %+v", active[1])
	}
	if inherited := parent.MergeWith(Sweatfile{}).ActivePreMergeExemptions(); len(inherited) != 2 {
		t.Errorf("expected inherit, got %+v", inherited)
	}
}
