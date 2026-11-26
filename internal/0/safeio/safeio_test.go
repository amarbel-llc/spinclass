package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadRegularReadsAFileInARealDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".spinclass")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRegular(path, MaxReadSize)
	if err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("got %q, %v", got, err)
	}
}

// B-L1: O_NOFOLLOW guards only the last element, so a symlinked parent (the
// worker replaced .spinclass with a link to a directory of its own) is refused.
func TestReadRegularRefusesASymlinkedParentDirectory(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "state.json"), []byte(`{"planted":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".spinclass")
	if err := os.Symlink(scratch, link); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadRegular(filepath.Join(link, "state.json"), MaxReadSize); err == nil {
		t.Fatalf("read %q through a symlinked parent, want a refusal", got)
	}
}
