package sweatfile_test

import (
	"testing"

	. "code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

func TestParseHooksAllowStaleBase(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[hooks]\nallow-stale-base = true\n"))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks == nil || sf.Hooks.AllowStaleBase == nil || !*sf.Hooks.AllowStaleBase {
		t.Fatalf("hooks.allow-stale-base: got %+v", sf.Hooks)
	}
	if !sf.AllowStaleBase() {
		t.Error("AllowStaleBase() = false, want true")
	}
	// The regenerated tommy decoder must consume the key, else `sc validate`
	// would flag it as unknown.
	if u := doc.Undecoded(); len(u) != 0 {
		t.Errorf("allow-stale-base left undecoded: %v", u)
	}
}

func TestAllowStaleBaseDefaultsFalse(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[hooks]\npre-merge = \"just\"\n"))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if doc.Data().AllowStaleBase() {
		t.Error("AllowStaleBase() = true with the key absent; the safe default is to enforce")
	}
	if (Sweatfile{}).AllowStaleBase() {
		t.Error("AllowStaleBase() = true on a zero Sweatfile")
	}
}

// The merge clause is the easiest part of a new knob to omit, and omitting it
// fails silently: the key parses, the accessor works on a single layer, and only
// inheritance from an outer sweatfile quietly stops happening.
func TestMergeAllowStaleBase(t *testing.T) {
	yes, no := true, false

	t.Run("inherits when the inner layer is silent", func(t *testing.T) {
		base := Sweatfile{Hooks: &Hooks{AllowStaleBase: &yes}}
		if !base.MergeWith(Sweatfile{}).AllowStaleBase() {
			t.Error("an inner layer with no [hooks] table dropped the inherited value")
		}
		if !base.MergeWith(Sweatfile{Hooks: &Hooks{}}).AllowStaleBase() {
			t.Error("an inner layer with an empty [hooks] table dropped the inherited value")
		}
	})

	t.Run("inner layer overrides", func(t *testing.T) {
		base := Sweatfile{Hooks: &Hooks{AllowStaleBase: &yes}}
		repo := Sweatfile{Hooks: &Hooks{AllowStaleBase: &no}}
		if base.MergeWith(repo).AllowStaleBase() {
			t.Error("an explicit false did not override an inherited true")
		}
	})

	t.Run("sets when only the inner layer has it", func(t *testing.T) {
		merged := Sweatfile{}.MergeWith(Sweatfile{Hooks: &Hooks{AllowStaleBase: &yes}})
		if !merged.AllowStaleBase() {
			t.Error("the inner layer's value was not applied")
		}
	})
}
