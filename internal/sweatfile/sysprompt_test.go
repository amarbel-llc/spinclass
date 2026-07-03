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
