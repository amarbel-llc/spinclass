package sysprompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The design-record scan runs in the serve, outside any confinement unit, over
// the session worktree, which the worker can write. It must read only inside
// the root: a symlinked index dir, a symlinked record file, or a configured dir
// that climbs out would hand the worker the titles of files it cannot read.

func writeAcceptedRecord(t *testing.T, dir, name, heading string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nstatus: accepted\n---\n\n# " + heading + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDesignRecordsIgnoreASymlinkedIndexDirLeavingTheRoot(t *testing.T) {
	outside := t.TempDir()
	writeAcceptedRecord(t, outside, "0001-secret.md", "OUTSIDE-SECRET-TITLE")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "docs", "features")); err != nil {
		t.Fatal(err)
	}

	got := renderDesignRecords(root, []string{"docs/features"})
	if strings.Contains(got, "OUTSIDE-SECRET-TITLE") {
		t.Errorf("a symlinked index dir leaked a record from outside the root:\n%s", got)
	}
}

func TestDesignRecordsIgnoreASymlinkedRecordFile(t *testing.T) {
	outside := t.TempDir()
	writeAcceptedRecord(t, outside, "target.md", "OUTSIDE-SECRET-TITLE")
	root := t.TempDir()
	dir := filepath.Join(root, "docs", "features")
	writeAcceptedRecord(t, dir, "0002-real.md", "Real record")
	if err := os.Symlink(filepath.Join(outside, "target.md"), filepath.Join(dir, "0003-link.md")); err != nil {
		t.Fatal(err)
	}

	got := renderDesignRecords(root, []string{"docs/features"})
	if strings.Contains(got, "OUTSIDE-SECRET-TITLE") {
		t.Errorf("a symlinked record file leaked a title from outside the root:\n%s", got)
	}
	if !strings.Contains(got, "Real record") {
		t.Errorf("the real record inside the root is missing:\n%s", got)
	}
}

func TestDesignRecordsRefuseAConfiguredDirOutsideTheRoot(t *testing.T) {
	parent := t.TempDir()
	writeAcceptedRecord(t, filepath.Join(parent, "elsewhere"), "0004-secret.md", "OUTSIDE-SECRET-TITLE")
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{"../elsewhere", filepath.Join(parent, "elsewhere")} {
		got := renderDesignRecords(root, []string{dir})
		if strings.Contains(got, "OUTSIDE-SECRET-TITLE") {
			t.Errorf("configured dir %q read outside the root:\n%s", dir, got)
		}
		if !strings.Contains(got, "outside the repository") {
			t.Errorf("configured dir %q: want a diagnostic naming why it was not read, got:\n%s", dir, got)
		}
	}
}
