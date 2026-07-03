package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/testgit"
)

func TestRandomNameFormat(t *testing.T) {
	repoPath := t.TempDir()

	name := RandomName(repoPath)
	parts := strings.SplitN(name, "-", 2)
	if len(parts) != 2 {
		t.Fatalf("RandomName() = %q, want adjective-noun format", name)
	}
	if parts[0] == "" || parts[1] == "" {
		t.Fatalf("RandomName() = %q, has empty adjective or noun", name)
	}
}

func TestRandomNameAvoidsCollision(t *testing.T) {
	repoPath := t.TempDir()
	wtDir := filepath.Join(repoPath, WorktreesDir)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Generate a name, then create a directory with that name to force collision
	first := RandomName(repoPath)
	if err := os.Mkdir(filepath.Join(wtDir, first), 0o755); err != nil {
		t.Fatal(err)
	}

	// Generate many names — none should equal the taken name
	for range 100 {
		name := RandomName(repoPath)
		if name == first {
			t.Fatalf("RandomName() returned colliding name %q", name)
		}
	}
}

// TestRandomNameAvoidsBranchCollision covers #207: a stale branch whose
// worktree directory was removed must still count as a name collision, so a
// fresh session never draws a name that `git worktree add` would silently
// adopt (checking out the stale branch instead of cutting a new one).
//
// The word space is shrunk to two possible names so the collision is exercised
// deterministically — with the full 2500-name space an unfixed RandomName only
// redraws the collided name ~4% of the time per 100-draw loop, making the test
// pass by luck rather than by the fix.
func TestRandomNameAvoidsBranchCollision(t *testing.T) {
	testgit.RequireGit(t)
	repoPath := t.TempDir()
	testgit.MustInit(t, repoPath)

	origAdj, origNoun := adjectives, nouns
	t.Cleanup(func() { adjectives, nouns = origAdj, origNoun })
	adjectives = []string{"solo"}
	nouns = []string{"pine", "oak"}

	// Branch on one of the two possible names, leaving no worktree directory —
	// the lingering-branch scenario. RandomName must then only ever yield the
	// other name.
	const taken = "solo-pine"
	if out, err := exec.Command("git", "-C", repoPath, "branch", taken).CombinedOutput(); err != nil {
		t.Fatalf("git branch %s: %v\n%s", taken, err, out)
	}

	for range 50 {
		if name := RandomName(repoPath); name == taken {
			t.Fatalf("RandomName() returned name %q colliding with existing branch", name)
		}
	}
}

func TestRandomNameUsesValidWords(t *testing.T) {
	repoPath := t.TempDir()

	adjSet := make(map[string]bool, len(adjectives))
	for _, a := range adjectives {
		adjSet[a] = true
	}
	nounSet := make(map[string]bool, len(nouns))
	for _, n := range nouns {
		nounSet[n] = true
	}

	for range 50 {
		name := RandomName(repoPath)
		parts := strings.SplitN(name, "-", 2)
		if !adjSet[parts[0]] {
			t.Errorf("adjective %q not in word list", parts[0])
		}
		if !nounSet[parts[1]] {
			t.Errorf("noun %q not in word list", parts[1])
		}
	}
}
