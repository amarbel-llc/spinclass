package hooks

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/worktree"
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
	policy := resolveWorktreePolicy(home, mainRepoRoot)

	return Run(stdin, stdout, mainRepoRoot, cwd, policy.disallowMainWorktree, WithWorktreePathRewrite(policy.rewriteEnabled), WithDenySweatfileEdits(policy.denySweatfileEdits))
}

// worktreePolicy is the PreToolUse policy resolved for a worktree session.
type worktreePolicy struct {
	disallowMainWorktree bool
	rewriteEnabled       bool
	denySweatfileEdits   bool
}

// resolveWorktreePolicy reads the policy from the main checkout's hierarchy;
// the worktree's own sweatfile is not a layer, so a corrupt or hostile one
// changes nothing. Worktree path rewrite (#176) is on by default; the sweatfile
// opt-out flips it.
func resolveWorktreePolicy(home, mainRepoRoot string) worktreePolicy {
	policy := worktreePolicy{
		rewriteEnabled:     true,
		denySweatfileEdits: denySweatfileEdits(home, mainRepoRoot),
	}
	if home != "" {
		if result, err := sweatfileio.LoadHierarchy(home, mainRepoRoot); err == nil {
			policy.disallowMainWorktree = result.Merged.DisallowMainWorktreeEnabled()
			policy.rewriteEnabled = !result.Merged.WorktreePathRewriteDisabled()
		}
	}
	return policy
}

func gitToplevel(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
