package close

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/nixgc"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sessionpick"
	"github.com/amarbel-llc/spinclass/internal/tapblock"
	"github.com/amarbel-llc/spinclass/internal/worktree"

	tap "github.com/amarbel-llc/tap/go"
)

// Run closes a session. dbg, when non-nil, is forwarded to the
// interactive picker (and on through to session.ListAll/ListForRepo) so
// excluded index entries are logged at Debug level. Pass nil for silent
// operation.
//
// nixGCOverride, when non-nil, overrides the [hooks].disable-nix-gc setting
// from the sweatfile cascade for this single invocation. true forces the gc
// to run; false skips it. nil defers to sweatfile.
func Run(w io.Writer, target string, force bool, nixGCOverride *bool, format string, dbg *slog.Logger) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	repoPath, wtPath, branch, err := resolveTarget(cwd, target, dbg)
	if err != nil {
		return err
	}

	return RunResolved(w, repoPath, wtPath, branch, force, nixGCOverride, format)
}

// RunResolved closes a session whose paths have already been resolved
// by the caller. Equivalent to Run minus the cwd/target lookup; callers
// that already know the worktree (e.g. internal/shop's exit handler)
// delegate cleanup here without re-resolving.
func RunResolved(w io.Writer, repoPath, wtPath, branch string, force bool, nixGCOverride *bool, format string) error {
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

	// Capture the worktree's nix gc roots and their closure BEFORE removing
	// the worktree (so the auto-roots' link targets still resolve). The reap
	// itself runs AFTER the worktree is gone, so the auto-roots are dangling
	// and the underlying store paths are no longer kept alive — `nix-store
	// --delete` can then succeed (or refuse if other roots still hold them).
	gcPlan := planNixGC(repoPath, wtPath, nixGCOverride)

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
	}

	if gcPlan != nil {
		runReap(tw, *gcPlan, branch)
	}

	if tw != nil {
		tw.Plan()
	}

	return nil
}

// runReap executes nixgc.Reap as a single TAP test point. nix-store
// stdout+stderr stream live into the TAP OutputBlock so the operator
// can observe per-path progress (and notice if a path stalls). When
// no TAP writer is attached (e.g. table format), the reap still runs
// but its output goes to os.Stderr — silent gc would defeat the point
// of moving this into a visible test point.
func runReap(tw *tap.Writer, plan nixgc.Plan, branch string) {
	desc := fmt.Sprintf("nix-gc reap %s", branch)
	if tw == nil {
		nixgc.Reap(plan, os.Stderr, os.Stderr)
		return
	}
	var summary nixgc.Summary
	tw.OutputBlock(desc, func(ob *tap.OutputBlockWriter) *tap.Diagnostics {
		lw := tapblock.NewLineWriter(ob)
		summary = nixgc.Reap(plan, lw, lw)
		lw.Flush()
		extras := map[string]any{
			"reclaimed": summary.Reclaimed,
			"kept":      summary.Kept,
			"closure":   len(plan.Closure),
		}
		if len(summary.Errors) > 0 {
			extras["errors"] = len(summary.Errors)
			extras["first_error"] = summary.Errors[0].Error()
			return &tap.Diagnostics{
				Severity: "warn",
				Message:  fmt.Sprintf("%d path(s) failed to delete", len(summary.Errors)),
				Extras:   extras,
			}
		}
		return &tap.Diagnostics{Extras: extras}
	})
}

// planNixGC captures the worktree's nix gc roots before removal. Returns nil
// when gc should not run (sweatfile/override disabled, nix not installed, no
// roots in the worktree, or any error during plan construction). The reap
// step runs only when this returns non-nil.
func planNixGC(repoPath, wtPath string, override *bool) *nixgc.Plan {
	enabled := !nixgc.Disabled(repoPath, wtPath)
	if override != nil {
		enabled = *override
	}
	if !enabled {
		return nil
	}
	plan, err := nixgc.NewPlan(wtPath)
	if errors.Is(err, nixgc.ErrNixUnavailable) {
		return nil
	}
	if err != nil {
		// Don't surface plan-build errors as TAP — they're noise to non-nix
		// users. Failing to enumerate just means we'll do nothing, which is
		// the safe default.
		return nil
	}
	if len(plan.Roots) == 0 {
		return nil
	}
	return &plan
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
