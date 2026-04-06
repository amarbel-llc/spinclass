package hooks

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

// Handle is the cobra-free entry point for the PreToolUse hook. It detects
// whether the working directory is inside a worktree, loads the sweatfile
// hierarchy to determine the disallow-main-worktree flag, and then runs the
// hook decision logic via Run.
func Handle(stdin io.Reader, stdout io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}

	if !worktree.IsWorktree(cwd) {
		toplevel, err := gitToplevel(cwd)
		if err != nil {
			return nil
		}
		if !worktree.IsWorktree(toplevel) {
			return Run(stdin, stdout, "", "", false)
		}
		cwd = toplevel
	}

	mainRepoRoot, err := git.CommonDir(cwd)
	if err != nil {
		return nil
	}

	home, _ := os.UserHomeDir()
	var disallowMainWorktree bool
	if home != "" {
		result, err := sweatfile.LoadWorktreeHierarchy(home, mainRepoRoot, cwd)
		if err == nil {
			disallowMainWorktree = result.Merged.DisallowMainWorktreeEnabled()
		}
	}

	return Run(stdin, stdout, mainRepoRoot, cwd, disallowMainWorktree)
}

func gitToplevel(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
