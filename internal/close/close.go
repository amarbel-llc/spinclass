package close

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/huh"

	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sessionpick"
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

	defaultBranch, dbErr := git.DefaultBranch(repoPath)
	unintegrated := dbErr == nil && git.CommitsAhead(wtPath, defaultBranch, branch) > 0
	dirty := git.StatusPorcelain(wtPath) != ""

	if (unintegrated || dirty) && !force {
		reason := describeUnintegrated(branch, unintegrated, dirty)
		var proceed bool
		err := huh.NewConfirm().
			Title(reason + " Close anyway?").
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

func describeUnintegrated(branch string, unintegrated, dirty bool) string {
	switch {
	case unintegrated && dirty:
		return fmt.Sprintf("Branch %q has unintegrated commits and uncommitted changes.", branch)
	case unintegrated:
		return fmt.Sprintf("Branch %q has commits not yet integrated into the default branch.", branch)
	default:
		return fmt.Sprintf("Branch %q has uncommitted changes.", branch)
	}
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
		s, ferr := session.FindByID(target)
		if ferr != nil {
			return "", "", "", fmt.Errorf(
				"no spinclass session for ID %q; if this is a bare git worktree, remove it with `git worktree remove`",
				target,
			)
		}
		return s.RepoPath, s.WorktreePath, s.Branch, nil
	}

	picked, err := sessionpick.Choose(repoPath, "close")
	if err != nil {
		return "", "", "", err
	}
	return picked.RepoPath, picked.WorktreePath, picked.Branch, nil
}
