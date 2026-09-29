package sweatfileio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

const layerLabel = "abc1234:sweatfile"

func layerRepo(t *testing.T) (home, repoDir string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	repoDir = filepath.Join(home, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	root := "[[post-merge]]\nname = \"krone\"\ncommand = \"echo root\"\n"
	if err := os.WriteFile(filepath.Join(repoDir, "sweatfile"), []byte(root), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, repoDir
}

func lastSource(t *testing.T, h sweatfile.Hierarchy) sweatfile.LoadSource {
	t.Helper()
	if len(h.Sources) == 0 {
		t.Fatal("no sources")
	}
	return h.Sources[len(h.Sources)-1]
}

func TestLoadHierarchyWithLayerOverridesRepoLayer(t *testing.T) {
	home, repoDir := layerRepo(t)
	layer := []byte("[[post-merge]]\nname = \"krone\"\ncommand = \"echo layer\"\n")

	h, err := LoadHierarchyWithLayer(home, repoDir, layerLabel, layer, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Merged.ActivePostMergeTargets()[0].Command; got != "echo layer" {
		t.Fatalf("command = %q, want echo layer", got)
	}
	if src := lastSource(t, h); src.Path != layerLabel || !src.Found {
		t.Fatalf("last source = %+v", src)
	}
}

func TestLoadHierarchyWithLayerNotFoundIsInert(t *testing.T) {
	home, repoDir := layerRepo(t)

	h, err := LoadHierarchyWithLayer(home, repoDir, layerLabel, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Merged.ActivePostMergeTargets()[0].Command; got != "echo root" {
		t.Fatalf("command = %q, want echo root", got)
	}
	if src := lastSource(t, h); src.Path != layerLabel || src.Found {
		t.Fatalf("last source = %+v", src)
	}
}

func TestLoadHierarchyWithLayerParseErrorWhenFound(t *testing.T) {
	home, repoDir := layerRepo(t)

	_, err := LoadHierarchyWithLayer(home, repoDir, layerLabel, []byte("[[post-merge]\nbroken"), true)
	if err == nil {
		t.Fatal("want a parse error")
	}
	if !strings.Contains(err.Error(), layerLabel) {
		t.Fatalf("error %q should name the layer %q", err, layerLabel)
	}
}
