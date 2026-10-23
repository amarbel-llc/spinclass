package sweatfile

import "testing"

// #351: merging one base with two different overrides must leave the base's
// [hooks] untouched and must not carry the first override into the second
// result. [auth] already merges by copy; [hooks] must too.
func TestMergeWithDoesNotMutateTheBaseHooks(t *testing.T) {
	strp := func(s string) *string { return &s }
	base := Sweatfile{Hooks: &Hooks{PreMerge: strp("just")}}

	first := base.MergeWith(Sweatfile{Hooks: &Hooks{Repair: strp("repair-one")}})
	second := base.MergeWith(Sweatfile{Hooks: &Hooks{PostMerge: strp("deploy")}})

	if base.Hooks.Repair != nil || base.Hooks.PostMerge != nil {
		t.Errorf("the base was mutated: repair=%v post-merge=%v", base.Hooks.Repair, base.Hooks.PostMerge)
	}
	if second.Hooks.Repair != nil {
		t.Errorf("the second merge carries the first override's repair %q", *second.Hooks.Repair)
	}
	if first.Hooks.Repair == nil || *first.Hooks.Repair != "repair-one" || first.Hooks.PostMerge != nil {
		t.Errorf("first merge = %+v", *first.Hooks)
	}
	if second.Hooks.PreMerge == nil || *second.Hooks.PreMerge != "just" {
		t.Errorf("second merge lost the base pre-merge: %+v", *second.Hooks)
	}
}
