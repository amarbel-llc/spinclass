package close

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sessionpick"
	"github.com/amarbel-llc/spinclass/internal/worktree"

	tap "github.com/amarbel-llc/bob/packages/tap-dancer/go"
)

// Run closes a session. dbg, when non-nil, is forwarded to the
// interactive picker (and on through to session.ListAll/ListForRepo) so
// excluded index entries are logged at Debug level. Pass nil for silent
// operation.
func Run(w io.Writer, target string, force bool, format string, dbg *slog.Logger) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	repoPath, wtPath, branch, err := resolveTarget(cwd, target, dbg)
	if err != nil {
		return err
	}

	var tw *tap.Writer
	if format == "tap" {
		tw = tap.NewWriter(w)
	}

	// Snapshot the state's PID before any teardown so we can wait on
	// the active spinclass process to exit before tombstoning. If the
	// state file is missing, PID stays 0 and WaitForExit is a no-op.
	var activePID int
	if st, rerr := session.Read(repoPath, branch); rerr == nil {
		activePID = st.PID
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

	// Wait briefly for the spinclass session to wind down so its final
	// state write lands before we tombstone (and then for the worktree
	// dir to be free for git to remove). Best-effort: WaitForExit
	// returns when the PID is gone or the deadline passes.
	executor.WaitForExit(activePID, 2*time.Second)

	// Promote the index symlink to a tombstone (regular file) and
	// remove the worktree-local state.json + .spinclass/ before the
	// worktree directory is itself force-removed by git. Best effort:
	// if the state file is gone (legacy session that never wrote one,
	// or an external worktree removal), skip the tombstone.
	if tombErr := session.Tombstone(repoPath, branch); tombErr != nil {
		// Fall through to Remove for cleanup; tombstone failures don't
		// block close.
		_ = session.Remove(repoPath, branch)
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

func resolveTarget(cwd, target string, dbg *slog.Logger) (repoPath, wtPath, branch string, err error) {
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

	picked, err := sessionpick.Choose(repoPath, "close", dbg)
	if err != nil {
		return "", "", "", err
	}
	return picked.RepoPath, picked.WorktreePath, picked.Branch, nil
}
