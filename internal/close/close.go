package close

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/charmbracelet/huh"

	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/worktree"

	tap "github.com/amarbel-llc/bob/packages/tap-dancer/go"
)

func Run(w io.Writer, target string, force bool, format string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	repoPath, wtPath, branch, err := resolveTarget(cwd, target)
	if err != nil {
		return err
	}

	var tw *tap.Writer
	if format == "tap" {
		tw = tap.NewWriter(w)
	}

	// Request graceful close if session is active.
	executor.RequestClose(repoPath, branch)

	pushed := git.RemoteBranchExists(repoPath, branch)
	if !pushed && !force {
		var proceed bool
		err := huh.NewConfirm().
			Title(fmt.Sprintf("Branch %q has not been pushed upstream. Close anyway?", branch)).
			Value(&proceed).
			Run()
		if err != nil {
			return fmt.Errorf("confirmation cancelled")
		}
		if !proceed {
			if tw != nil {
				tw.Skip("close "+branch, "user declined")
				tw.Plan()
			}
			return nil
		}
	}

	if err := git.WorktreeForceRemove(repoPath, wtPath); err != nil {
		diag := map[string]string{"error": err.Error()}
		if tw != nil {
			tw.NotOk("remove worktree "+branch, diag)
			tw.Plan()
		}
		return fmt.Errorf("removing worktree %s: %w", branch, err)
	}

	if _, err := git.BranchForceDelete(repoPath, branch); err != nil {
		diag := map[string]string{"error": err.Error()}
		if tw != nil {
			tw.NotOk("delete branch "+branch, diag)
			tw.Plan()
		}
		return fmt.Errorf("deleting branch %s: %w", branch, err)
	}

	session.Remove(repoPath, branch)

	if tw != nil {
		tw.Ok("close " + branch)
		tw.Plan()
	}

	return nil
}

func resolveTarget(cwd, target string) (repoPath, wtPath, branch string, err error) {
	if worktree.IsWorktree(cwd) && target == "" {
		repoPath, err = git.CommonDir(cwd)
		if err != nil {
			return "", "", "", fmt.Errorf("not in a worktree directory: %s", cwd)
		}
		branch, err = git.BranchCurrent(cwd)
		if err != nil {
			return "", "", "", fmt.Errorf("could not determine current branch: %w", err)
		}
		return repoPath, cwd, branch, nil
	}

	if worktree.IsWorktree(cwd) {
		repoPath, err = git.CommonDir(cwd)
	} else {
		repoPath, err = worktree.DetectRepo(cwd)
	}
	if err != nil {
		return "", "", "", fmt.Errorf("not in a git repository: %s", cwd)
	}

	if target != "" {
		paths := worktree.ListWorktrees(repoPath)
		for _, p := range paths {
			if filepath.Base(p) == target {
				return repoPath, p, target, nil
			}
		}
		return "", "", "", fmt.Errorf("worktree not found: %s", target)
	}

	wtPath, branch, err = chooseWorktree(repoPath)
	if err != nil {
		return "", "", "", err
	}
	return repoPath, wtPath, branch, nil
}

func chooseWorktree(repoPath string) (wtPath, branch string, err error) {
	paths := worktree.ListWorktrees(repoPath)
	if len(paths) == 0 {
		return "", "", fmt.Errorf("no worktrees found in %s", repoPath)
	}

	branches := make([]string, len(paths))
	for i, p := range paths {
		branches[i] = filepath.Base(p)
	}

	var selected string
	options := make([]huh.Option[string], len(branches))
	for i, b := range branches {
		options[i] = huh.NewOption(b, b)
	}

	err = huh.NewSelect[string]().
		Title("Select worktree to close").
		Options(options...).
		Value(&selected).
		Run()
	if err != nil {
		return "", "", fmt.Errorf("worktree selection cancelled")
	}

	for i, b := range branches {
		if b == selected {
			return paths[i], selected, nil
		}
	}
	return "", "", fmt.Errorf("worktree not found: %s", selected)
}
