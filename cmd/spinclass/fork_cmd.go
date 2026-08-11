package main

import (
	"fmt"
	"os"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// resolveForkSource resolves the fork SOURCE worktree from a directory, as
// `sc fork` (create-only) always has: detect the repo, read the current
// branch, and require the standard .worktrees/<branch> layout.
//
// (The detached-worker fork that also used this — `sc fork --brief` /
// `fork-session` — was removed in spinclass#262; `sc spawn` with its now-optional
// repo covers launching a detached worker in the current repo.)
func resolveForkSource(sourceDir string) (worktree.ResolvedPath, error) {
	repoPath, err := worktree.DetectRepo(sourceDir)
	if err != nil {
		return worktree.ResolvedPath{}, err
	}

	currentBranch, err := git.BranchCurrent(sourceDir)
	if err != nil {
		return worktree.ResolvedPath{}, fmt.Errorf("could not determine current branch in %s: %w", sourceDir, err)
	}

	currentPath := filepath.Join(repoPath, worktree.WorktreesDir, currentBranch)
	if _, err := os.Stat(currentPath); os.IsNotExist(err) {
		return worktree.ResolvedPath{}, fmt.Errorf(
			"worktree path %s does not exist; fork requires a standard .worktrees layout",
			currentPath,
		)
	}

	return worktree.ResolvedPath{
		AbsPath:    currentPath,
		RepoPath:   repoPath,
		Branch:     currentBranch,
		SessionKey: filepath.Base(repoPath) + "/" + currentBranch,
	}, nil
}
