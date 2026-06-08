package sweatfileio

import (
	"testing"

	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

// TestDecodeEmptyVsAbsentArrays locks the behavior the deleted consumed-map
// nil-normalization used to provide and that tommy's generated decoder now
// provides natively: a present-but-empty array decodes to a NON-NIL empty
// slice, while an absent key decodes to nil. sweatfile.MergeWith depends on
// this nil-vs-empty distinction (nil = inherit, empty = clear), so this is the
// load-bearing contract behind dropping the normalization.
func TestDecodeEmptyVsAbsentArrays(t *testing.T) {
	// Present-but-empty. allowed-mcps is top-level so it must precede any table
	// header (TOML scopes bare keys to the most recent table).
	present := `allowed-mcps = []
[claude]
allow = []
[git]
excludes = []
[direnv]
envrc = []
`
	doc, err := Parse([]byte(present))
	if err != nil {
		t.Fatalf("parse present-empty: %v", err)
	}
	sf := doc.Data()
	if sf.Claude == nil || sf.Claude.Allow == nil || len(sf.Claude.Allow) != 0 {
		t.Errorf("claude.allow present-empty: want non-nil empty slice, got %#v", sf.Claude)
	}
	if sf.Git == nil || sf.Git.Excludes == nil || len(sf.Git.Excludes) != 0 {
		t.Errorf("git.excludes present-empty: want non-nil empty slice, got %#v", sf.Git)
	}
	if sf.Direnv == nil || sf.Direnv.Envrc == nil || len(sf.Direnv.Envrc) != 0 {
		t.Errorf("direnv.envrc present-empty: want non-nil empty slice, got %#v", sf.Direnv)
	}
	if sf.AllowedMCPs == nil || len(sf.AllowedMCPs) != 0 {
		t.Errorf("allowed-mcps present-empty: want non-nil empty slice, got %#v", sf.AllowedMCPs)
	}

	// Absent: a sweatfile without those keys leaves the slices nil.
	doc2, err := Parse([]byte("[hooks]\npre-merge = \"just\"\n"))
	if err != nil {
		t.Fatalf("parse absent: %v", err)
	}
	sf2 := doc2.Data()
	if sf2.Claude != nil && sf2.Claude.Allow != nil {
		t.Errorf("claude.allow absent: want nil, got %#v", sf2.Claude.Allow)
	}
	if sf2.AllowedMCPs != nil {
		t.Errorf("allowed-mcps absent: want nil, got %#v", sf2.AllowedMCPs)
	}

	// MergeWith consequence: present-empty clears an inherited value; absent
	// inherits it.
	base := sweatfile.Sweatfile{AllowedMCPs: []string{"inherited"}}
	if merged := base.MergeWith(*doc.Data()); len(merged.AllowedMCPs) != 0 {
		t.Errorf("present-empty allowed-mcps should clear inherited, got %#v", merged.AllowedMCPs)
	}
	if merged := base.MergeWith(*doc2.Data()); len(merged.AllowedMCPs) != 1 {
		t.Errorf("absent allowed-mcps should inherit, got %#v", merged.AllowedMCPs)
	}
}
