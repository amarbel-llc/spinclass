package merge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCommitHierarchyIgnoresUncommittedEdits(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	commitSweatfile(t, wtPath, "[[post-merge]]\nname = \"krone\"\ncommand = \"echo COMMITTED\"\n")
	head := runGit(t, wtPath, "rev-parse", "HEAD")
	edited := "[[post-merge]]\nname = \"krone\"\ncommand = \"echo EDITED\"\n"
	if err := os.WriteFile(filepath.Join(wtPath, "sweatfile"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	h, err := loadCommitHierarchy(os.Getenv("HOME"), repoDir, head)
	if err != nil {
		t.Fatal(err)
	}
	targets := h.Merged.ActivePostMergeTargets()
	if len(targets) != 1 || targets[0].Command != "echo COMMITTED" {
		t.Fatalf("targets = %+v, want echo COMMITTED", targets)
	}

	h, err = loadCommitHierarchy(os.Getenv("HOME"), repoDir, head+"~1")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Merged.ActivePostMergeTargets(); len(got) != 0 {
		t.Fatalf("parent targets = %+v, want none", got)
	}
}
