package sweatfile_test

import (
	"strings"
	"testing"

	. "code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

func TestRequireHookScopeDefaultsFalse(t *testing.T) {
	no := false
	for name, sf := range map[string]Sweatfile{
		"no [hooks] table": {},
		"key absent":       {Hooks: &Hooks{}},
		"explicit false":   {Hooks: &Hooks{RequireHookScope: &no}},
	} {
		if sf.RequireHookScope() {
			t.Errorf("%s: RequireHookScope() = true; the unscoped fallback is the default", name)
		}
	}
}

func TestRequireHookScopeParsesAndMerges(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[hooks]\nrequire-hook-scope = true\n"))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !doc.Data().RequireHookScope() {
		t.Fatalf("require-hook-scope = true not reported: %+v", doc.Data().Hooks)
	}
	// The regenerated tommy decoder must consume the key, else `sc validate`
	// would flag it as unknown.
	if u := doc.Undecoded(); len(u) != 0 {
		t.Errorf("require-hook-scope left undecoded: %v", u)
	}

	// A fresh global layer per merge: MergeWith writes [hooks] overrides through
	// the receiver's shared *Hooks.
	yes, no := true, false
	global := func() Sweatfile { return Sweatfile{Hooks: &Hooks{RequireHookScope: &yes}} }
	if global().MergeWith(Sweatfile{Hooks: &Hooks{RequireHookScope: &no}}).RequireHookScope() {
		t.Error("a repo-layer false did not override the global true")
	}
	if !global().MergeWith(Sweatfile{}).RequireHookScope() {
		t.Error("an absent repo layer dropped the inherited true")
	}
	if !global().MergeWith(Sweatfile{Hooks: &Hooks{}}).RequireHookScope() {
		t.Error("a repo layer with an empty [hooks] table dropped the inherited true")
	}
}

// Guards a stale codec regen on the ENCODE side: a value set in memory must
// reach the document and decode back.
func TestRequireHookScopeRoundTrips(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[hooks]\npre-merge = \"just\"\n"))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	yes := true
	doc.Data().Hooks.RequireHookScope = &yes

	encoded, err := doc.Encode()
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}
	if !strings.Contains(string(encoded), "require-hook-scope = true") {
		t.Fatalf("encoded document lost the key:\n%s", encoded)
	}

	reparsed, err := sweatfileio.Parse(encoded)
	if err != nil {
		t.Fatalf("reparse error: %v", err)
	}
	if !reparsed.Data().RequireHookScope() {
		t.Errorf("require-hook-scope did not survive the round trip:\n%s", encoded)
	}
}
