package sweatfile

import (
	"reflect"
	"testing"
)

func syspromptDirs(dirs []string) Sweatfile {
	return Sweatfile{Sysprompt: &Sysprompt{DocIndexDirs: dirs}}
}

// [sysprompt].doc-index-dirs uses OVERRIDE (not append) merge semantics: a
// non-nil child value replaces the inherited list, an explicit [] disables the
// index, and a nil (unset) child inherits. See FDR 0021.
func TestSyspromptDocIndexDirsMerge(t *testing.T) {
	// nil child inherits the parent value.
	merged := syspromptDirs([]string{"docs/features"}).MergeWith(Sweatfile{})
	if dirs, ok := merged.SyspromptDocIndexDirs(); !ok || !reflect.DeepEqual(dirs, []string{"docs/features"}) {
		t.Errorf("nil child should inherit parent: got %v ok=%v", dirs, ok)
	}

	// non-empty child replaces (does NOT append).
	merged = syspromptDirs([]string{"docs/features"}).MergeWith(syspromptDirs([]string{"design"}))
	if dirs, _ := merged.SyspromptDocIndexDirs(); !reflect.DeepEqual(dirs, []string{"design"}) {
		t.Errorf("non-empty child should replace, not append: got %v", dirs)
	}

	// explicit [] child sets an empty list — the off switch.
	merged = syspromptDirs([]string{"docs/features"}).MergeWith(syspromptDirs([]string{}))
	if dirs, ok := merged.SyspromptDocIndexDirs(); !ok || len(dirs) != 0 {
		t.Errorf("empty child should set empty (disable): got %v ok=%v", dirs, ok)
	}

	// wholly unset reports not-set so the caller applies its built-in default.
	if dirs, ok := (Sweatfile{}).SyspromptDocIndexDirs(); ok || dirs != nil {
		t.Errorf("unset should report ok=false: got %v ok=%v", dirs, ok)
	}
}

// [sysprompt].man-index and .repo-index share doc-index-dirs' OVERRIDE merge
// semantics — they are scan roots, so a child replaces rather than accumulates
// and an explicit [] clears an inherited fleet-root selection. Unlike
// doc-index-dirs they have no built-in default, so unset simply leaves the
// index off. See FDR 0030.
func TestSyspromptIndexSourcesMerge(t *testing.T) {
	man := func(v []string) Sweatfile { return Sweatfile{Sysprompt: &Sysprompt{ManIndex: v}} }
	repo := func(v []string) Sweatfile { return Sweatfile{Sysprompt: &Sysprompt{RepoIndex: v}} }

	// nil child inherits the parent selection.
	merged := man([]string{"/eng/man"}).MergeWith(Sweatfile{})
	if got, ok := merged.SyspromptManIndex(); !ok || !reflect.DeepEqual(got, []string{"/eng/man"}) {
		t.Errorf("man-index nil child should inherit: got %v ok=%v", got, ok)
	}

	// non-empty child replaces rather than appending.
	merged = man([]string{"/eng/man"}).MergeWith(man([]string{"/other/man"}))
	if got, _ := merged.SyspromptManIndex(); !reflect.DeepEqual(got, []string{"/other/man"}) {
		t.Errorf("man-index non-empty child should replace: got %v", got)
	}

	// explicit [] clears an inherited selection — the off switch.
	merged = man([]string{"/eng/man"}).MergeWith(man([]string{}))
	if got, ok := merged.SyspromptManIndex(); !ok || len(got) != 0 {
		t.Errorf("man-index empty child should clear: got %v ok=%v", got, ok)
	}

	// repo-index behaves identically.
	merged = repo([]string{"/eng/repos"}).MergeWith(repo([]string{"/elsewhere"}))
	if got, _ := merged.SyspromptRepoIndex(); !reflect.DeepEqual(got, []string{"/elsewhere"}) {
		t.Errorf("repo-index non-empty child should replace: got %v", got)
	}

	// the two are independent: setting one must not disturb the other.
	merged = man([]string{"/eng/man"}).MergeWith(repo([]string{"/eng/repos"}))
	if got, _ := merged.SyspromptManIndex(); !reflect.DeepEqual(got, []string{"/eng/man"}) {
		t.Errorf("repo-index child must not clobber man-index: got %v", got)
	}
	if got, _ := merged.SyspromptRepoIndex(); !reflect.DeepEqual(got, []string{"/eng/repos"}) {
		t.Errorf("repo-index not carried through merge: got %v", got)
	}

	// wholly unset reports not-set, which leaves both indexes off.
	if got, ok := (Sweatfile{}).SyspromptManIndex(); ok || got != nil {
		t.Errorf("unset man-index should report ok=false: got %v ok=%v", got, ok)
	}
	if got, ok := (Sweatfile{}).SyspromptRepoIndex(); ok || got != nil {
		t.Errorf("unset repo-index should report ok=false: got %v ok=%v", got, ok)
	}
}

// [sysprompt].index-limit is a SCALAR override (the [hooks] shape), not an
// array: nil inherits, a set value replaces. Zero is a meaningful value —
// it removes the cap — so it must survive the merge rather than being
// mistaken for unset. See FDR 0030.
func TestSyspromptIndexLimitMerge(t *testing.T) {
	limit := func(n int) Sweatfile { return Sweatfile{Sysprompt: &Sysprompt{IndexLimit: &n}} }

	// nil child inherits the parent value.
	merged := limit(400).MergeWith(Sweatfile{})
	if got, ok := merged.SyspromptIndexLimit(); !ok || got != 400 {
		t.Errorf("nil child should inherit: got %v ok=%v", got, ok)
	}

	// a set child replaces.
	merged = limit(400).MergeWith(limit(50))
	if got, ok := merged.SyspromptIndexLimit(); !ok || got != 50 {
		t.Errorf("set child should replace: got %v ok=%v", got, ok)
	}

	// zero is a real value (uncapped), distinct from unset.
	merged = limit(400).MergeWith(limit(0))
	if got, ok := merged.SyspromptIndexLimit(); !ok || got != 0 {
		t.Errorf("zero must survive as a set value: got %v ok=%v", got, ok)
	}

	// wholly unset reports not-set so the caller applies its built-in default.
	if got, ok := (Sweatfile{}).SyspromptIndexLimit(); ok || got != 0 {
		t.Errorf("unset should report ok=false: got %v ok=%v", got, ok)
	}

	// index-limit and the source arrays are independent.
	merged = limit(75).MergeWith(Sweatfile{Sysprompt: &Sysprompt{ManIndex: []string{"/m"}}})
	if got, ok := merged.SyspromptIndexLimit(); !ok || got != 75 {
		t.Errorf("man-index child must not clobber index-limit: got %v ok=%v", got, ok)
	}
}
