package sysprompt

import (
	"path/filepath"
	"reflect"
	"testing"
)

// A spec is split on ":" after expansion, which is what makes a bare
// "$MANPATH" — itself a colon-joined list, and one that commonly carries empty
// leading entries — usable as a single selector.
func TestExpandSourcesSplitsColonListAndDropsEmpties(t *testing.T) {
	t.Setenv("TEST_MANPATH", "::/one:/two")

	got := expandSources([]string{"$TEST_MANPATH"})

	if want := []string{"/one", "/two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// An unset variable expands to empty and contributes nothing rather than
// yielding a bogus path.
func TestExpandSourcesUnsetVariableContributesNothing(t *testing.T) {
	if got := expandSources([]string{"$DEFINITELY_UNSET_FOR_TEST"}); len(got) != 0 {
		t.Errorf("unset variable must contribute nothing, got %v", got)
	}
}

// A leading ~ resolves against the home directory so sweatfile values stay
// portable across hosts.
func TestExpandSourcesExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := expandSources([]string{"~/share/man"})

	if want := []string{filepath.Join(home, "share", "man")}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Order is preserved and duplicates dropped, so a path reachable from two
// specs is indexed once.
func TestExpandSourcesDedupsPreservingOrder(t *testing.T) {
	got := expandSources([]string{"/b", "/a", "/b"})

	if want := []string{"/b", "/a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A glob is matched against the filesystem; a literal path is passed through
// untouched even when it does not exist, leaving existence checks to the
// caller that knows what the path is supposed to be.
func TestExpandSourcesGlobVersusLiteral(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "7", "one", ".SH NAME\none \\- x\n", false)

	got := expandSources([]string{filepath.Join(root, "man*", "*")})
	if want := []string{filepath.Join(root, "man7", "one.7")}; !reflect.DeepEqual(got, want) {
		t.Errorf("glob: got %v, want %v", got, want)
	}

	missing := filepath.Join(root, "definitely-absent")
	if got := expandSources([]string{missing}); !reflect.DeepEqual(got, []string{missing}) {
		t.Errorf("literal path must pass through, got %v", got)
	}
}

// formatIndex renders nothing at all when there is neither an entry nor a
// warning, so an index whose sources match nothing stays invisible rather than
// emitting a bare heading.
func TestFormatIndexEmptyRendersNothing(t *testing.T) {
	if out := formatIndex("Heading", "instruction", nil, nil, 0); out != "" {
		t.Errorf("empty index must render nothing, got:\n%s", out)
	}
}
