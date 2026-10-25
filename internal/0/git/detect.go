package git

import (
	"fmt"
	"os"
	"path/filepath"
)

// DetectRepo walks up from dir looking for a .git directory (must be a
// directory, not a file — files indicate worktrees). Respects
// GIT_CEILING_DIRECTORIES to prevent discovery above certain paths.
// Returns the repo root.
//
// It lives here, not in internal/worktree, because it is pure path-walking
// with no dependency on worktree setup: internal/session needs it to climb
// from a checkout subdirectory to the main-checkout root, and importing
// worktree for that one call dragged the whole config-and-setup stack under
// every package that touches session state (spinclass#307).
func DetectRepo(dir string) (string, error) {
	dir = filepath.Clean(dir)
	ceilings := parseCeilingDirs()

	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Lstat(gitPath)
		if err == nil && info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir || isCeiling(dir, ceilings) {
			return "", fmt.Errorf("no git repository found from %s", dir)
		}
		dir = parent
	}
}

func parseCeilingDirs() []string {
	env := os.Getenv("GIT_CEILING_DIRECTORIES")
	if env == "" {
		return nil
	}

	var dirs []string
	for _, d := range filepath.SplitList(env) {
		if clean := filepath.Clean(d); filepath.IsAbs(clean) {
			dirs = append(dirs, clean)
		}
	}
	return dirs
}

func isCeiling(dir string, ceilings []string) bool {
	for _, c := range ceilings {
		if dir == c {
			return true
		}
	}
	return false
}
