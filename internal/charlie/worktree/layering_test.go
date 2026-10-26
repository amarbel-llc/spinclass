package worktree

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// #308: worktree is the path half only (resolution, listing, naming) over
// git. Setup application lives in worktreesetup, so a change to any setup
// dependency no longer rebuilds every caller that only resolves paths.
func TestWorktreeImportsNoSetupDependencies(t *testing.T) {
	forbidden := []string{
		"internal/0/claude", "internal/0/embeds", "internal/0/madder", "internal/0/sweatfile",
		"internal/alfa/dodder", "internal/alfa/setupfingerprint", "internal/alfa/sweatfileio",
		"internal/bravo/apply", "internal/bravo/hookrun",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range forbidden {
				if strings.HasSuffix(path, "/"+bad) {
					t.Errorf("%s imports %s; setup belongs in worktreesetup", name, path)
				}
			}
		}
	}
}
