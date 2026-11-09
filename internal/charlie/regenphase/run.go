package regenphase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

// CommitSubject is the one commit the phase makes.
const CommitSubject = "chore: regenerate and format under updated flake.lock"

// runRepair runs a repair-shaped hook (a command in the worktree under its
// devshell, bounded by [hooks].repair-timeout), a seam so a test needs no
// direnv.
var runRepair = hookrun.Repair

// hookAttempts bounds how often a hook that exits nonzero is run again.
// conformist exits 1 after it reformatted files (the reformat is staged, so the
// next run finds them conformant and exits 0) and also when one lane errors
// transiently (a cache-db open timeout under load, seen live), so a second run
// settles both; a hook that still fails on the last attempt is a real failure.
const hookAttempts = 3

// runSettled runs the hook until it exits 0 or hookAttempts is spent, and
// returns the last error. The hook's output of every attempt accumulates in out.
func runSettled(ctx context.Context, sf sweatfile.Sweatfile, wtPath string, out io.Writer) error {
	var err error
	for i := 0; i < hookAttempts; i++ {
		if err = runRepair(ctx, sf, wtPath, out); err == nil || ctx.Err() != nil {
			return err
		}
	}
	return err
}

// asRepairHook is sf with its repair command replaced by cmd, so hookrun's
// repair runner (own process group, wall-clock cap, cancellation, direnv
// wrapping) runs it.
func asRepairHook(sf sweatfile.Sweatfile, cmd string) sweatfile.Sweatfile {
	var h sweatfile.Hooks
	if sf.Hooks != nil {
		h = *sf.Hooks
	}
	h.Repair = &cmd
	h.DisableRepair = nil
	sf.Hooks = &h
	return sf
}

// run is R0d of the plan: with the lock admitted (Phase did that), run the
// repo's regenerating hooks in the worktree and commit whatever they changed as
// ONE separate commit, so the generated churn is reviewable and revertable on
// its own and the branch's own commits stay as written.
//
// Why running the hooks here is the new-lock run: they run in the worktree under
// its devshell (direnv, when the worktree has an .envrc, with the caller's
// direnv state scrubbed), which evaluates the flake the worktree holds, and the
// admission judged the committed flake.nix and flake.lock at HEAD. A clean
// tracked tree makes the two the same tree, so a dirty one is refused here, as
// REPAIR refuses it (#345).
//
// Both hooks run when both are configured (operator direction, 2026-11-09), in
// this order:
//  1. [hooks].pre-commit (FDR 0019, e.g. conformist --staged), staged mode. It
//     only sees STAGED files, so HEAD is soft-reset to the merge base with the
//     landed branch, which stages the whole branch diff, flake.lock included:
//     that is what fires a lane triggered by a staged lock. The hook runs, HEAD
//     is put back, and the staged difference is committed (hooks bypassed: the
//     per-commit dispatcher would restamp with the frozen OLD formatter).
//  2. [hooks].repair (FDR 0018, e.g. conformist --commit --amend), over the
//     whole tree, on the now-clean tree. When step 1 made the commit, repair's
//     amend (or, for a repair that only edits files, the staged edits) is folded
//     into that same commit; otherwise it becomes the commit, with an amend of
//     the branch's own commit split off by a soft reset first.
//
// The union is one commit. The two overlap a little (both format), so the second
// hook re-doing the first's work is harmless: it finds the tree conformant. Only
// tracked files are taken. A hook that exits nonzero is run again (up to
// hookAttempts: a reformat settles on the second run, a transient lane error
// may not recur); one that still fails fails the phase and puts the tree back at
// the branch's HEAD, so a lane that never ran cannot pass as "conformant" and a
// half-regenerated commit is never made.
func run(ctx context.Context, ts *crap.TestStream, sf sweatfile.Sweatfile, wtPath, baseRef string) error {
	staged := sf.PreCommitActive()
	repair := sf.RepairActive()
	if !staged && !repair {
		ts.Skip(StepName, "neither [hooks].pre-commit nor [hooks].repair is configured, so there is nothing to run under the new lock")
		return nil
	}
	if dirty := trackedChanges(wtPath); dirty != "" {
		return failStep(ts, StepName,
			fmt.Errorf("the worktree has uncommitted tracked changes, so the tree is not the one the lock policy admitted: %s", dirty),
			"commit or discard them, or pass --no-regenerate")
	}
	sha0, err := git.RevParse(wtPath, "HEAD")
	if err != nil {
		return failStep(ts, StepName, fmt.Errorf("could not resolve HEAD: %w", err), "")
	}

	var (
		out       bytes.Buffer
		hookErrs  []error
		committed bool
	)
	hookCtx := hookrun.WithScrubbedDirenv(ctx)

	if staged {
		didCommit, hErr, stepErr := stagedStep(hookCtx, sf, wtPath, baseRef, sha0, &out)
		if stepErr != nil {
			return failStep(ts, StepName, stepErr, out.String())
		}
		committed = didCommit
		if hErr != nil {
			hookErrs = append(hookErrs, fmt.Errorf("[hooks].pre-commit: %w", hErr))
		}
	}
	if repair {
		didCommit, hErr, stepErr := repairStep(hookCtx, sf, wtPath, sha0, committed, &out)
		if stepErr != nil {
			return failStep(ts, StepName, stepErr, out.String())
		}
		committed = committed || didCommit
		if hErr != nil {
			hookErrs = append(hookErrs, fmt.Errorf("[hooks].repair: %w", hErr))
		}
	}

	// A hook that still errors after its retries is a lane that did not run
	// (live, 2026-11-09: a conformist cache-db timeout left the facade lane
	// undone while the file another lane restaged was committed as if the
	// regeneration had succeeded). The phase fails and the branch is put back as
	// written; the hook's own output (its WARN lines) goes in the failure.
	if hookErr := errors.Join(hookErrs...); hookErr != nil {
		restoreTree(wtPath, sha0)
		return failStep(ts, StepName, fmt.Errorf("a hook still failed under the new lock after %d attempts (the worktree was put back to %s): %w", hookAttempts, shortSha(sha0), hookErr), out.String())
	}
	if !committed {
		ts.Ok(StepName + " (already conformant under the new lock)")
		return nil
	}
	names, _ := git.Run(wtPath, "diff", "--name-only", sha0, "HEAD")
	sha2, _ := git.RevParse(wtPath, "HEAD")
	ts.Ok(fmt.Sprintf("%s (committed %s: %d file(s))", StepName, shortSha(sha2), len(nonEmptyLines(names))))
	return nil
}

// stagedStep runs [hooks].pre-commit over the branch's staged diff and commits
// what it changed. hookErr is the hook's own error (kept apart: it does not
// stop the phase); stepErr is a git failure that does.
func stagedStep(ctx context.Context, sf sweatfile.Sweatfile, wtPath, baseRef, sha0 string, out *bytes.Buffer) (committed bool, hookErr, stepErr error) {
	mb, mbErr := git.Run(wtPath, "merge-base", "HEAD", baseRef)
	mb = strings.TrimSpace(mb)
	if mbErr != nil || mb == "" {
		return false, nil, fmt.Errorf("no merge base between HEAD and %s, so the branch's diff cannot be staged for the hook: %v", baseRef, mbErr)
	}
	if mb != sha0 {
		if _, rErr := git.Run(wtPath, "reset", "-q", "--soft", mb); rErr != nil {
			return false, nil, fmt.Errorf("could not stage the branch's diff for the hook: %w", rErr)
		}
	}
	hookErr = runSettled(ctx, asRepairHook(sf, *sf.PreCommitHookCommand()), wtPath, out)

	// Put HEAD back to the branch's commit; the tree the hook left stays staged.
	if head, _ := git.RevParse(wtPath, "HEAD"); head != sha0 {
		if _, rErr := git.Run(wtPath, "reset", "-q", "--soft", sha0); rErr != nil {
			return false, hookErr, fmt.Errorf("could not put HEAD back to the branch's commit %s after the pre-commit hook: %w", shortSha(sha0), rErr)
		}
	}
	names, nErr := git.Run(wtPath, "diff", "--cached", "--name-only")
	if nErr != nil {
		return false, hookErr, fmt.Errorf("could not list the pre-commit hook's changes: %w", nErr)
	}
	if len(nonEmptyLines(names)) == 0 {
		// Nothing staged: leave a clean tree for the next hook (a failed hook
		// may have edited files without staging them).
		restoreTree(wtPath, sha0)
		return false, hookErr, nil
	}
	if _, cErr := git.Run(wtPath, "commit", "-q", "--no-verify", "-m", CommitSubject); cErr != nil {
		return false, hookErr, fmt.Errorf("could not commit the regenerated files: %w", cErr)
	}
	return true, hookErr, nil
}

// repairStep runs [hooks].repair on the (clean) tree and folds the result into
// the phase's commit: when stagedCommitted, into that commit (an amend by the
// tool already is; plain edits are staged and amended in); otherwise a new
// commit, after splitting off a tool amend of the branch's own commit.
func repairStep(ctx context.Context, sf sweatfile.Sweatfile, wtPath, sha0 string, stagedCommitted bool, out *bytes.Buffer) (committed bool, hookErr, stepErr error) {
	before, err := git.RevParse(wtPath, "HEAD")
	if err != nil {
		return false, nil, fmt.Errorf("could not resolve HEAD before the repair hook: %w", err)
	}
	hookErr = runSettled(ctx, asRepairHook(sf, *sf.RepairHookCommand()), wtPath, out)

	after, err := git.RevParse(wtPath, "HEAD")
	if err != nil {
		return false, hookErr, fmt.Errorf("could not resolve HEAD after the repair hook: %w", err)
	}
	switch {
	case stagedCommitted && after != before:
		// The tool amended the regenerate commit: that is the fold.
		return false, hookErr, nil
	case stagedCommitted:
		if _, aErr := git.Run(wtPath, "add", "-u"); aErr != nil {
			return false, hookErr, fmt.Errorf("could not stage the repair hook's edits: %w", aErr)
		}
		if !hasStaged(wtPath) {
			return false, hookErr, nil
		}
		if _, cErr := git.Run(wtPath, "commit", "-q", "--amend", "--no-edit", "--no-verify"); cErr != nil {
			return false, hookErr, fmt.Errorf("could not fold the repair hook's edits into the regenerate commit: %w", cErr)
		}
		return false, hookErr, nil
	case after != sha0:
		// The tool amended the branch's own commit: split it off.
		if _, rErr := git.Run(wtPath, "reset", "-q", "--soft", sha0); rErr != nil {
			return false, hookErr, fmt.Errorf("could not split the repair hook's amend from the branch's commit (HEAD is %s, expected %s): %w", after, sha0, rErr)
		}
	default:
		if _, aErr := git.Run(wtPath, "add", "-u"); aErr != nil {
			return false, hookErr, fmt.Errorf("could not stage the repair hook's edits: %w", aErr)
		}
	}
	if !hasStaged(wtPath) {
		restoreTree(wtPath, sha0)
		return false, hookErr, nil
	}
	if _, cErr := git.Run(wtPath, "commit", "-q", "--no-verify", "-m", CommitSubject); cErr != nil {
		return false, hookErr, fmt.Errorf("could not commit the regenerated files: %w", cErr)
	}
	return true, hookErr, nil
}

// hasStaged reports whether the index differs from HEAD.
func hasStaged(wtPath string) bool {
	names, err := git.Run(wtPath, "diff", "--cached", "--name-only")
	return err == nil && len(nonEmptyLines(names)) > 0
}

// restoreTree puts the worktree back at sha, discarding what a hook left in the
// index and tree. It is only called on a tree that was clean at sha (run
// refuses a dirty one first, and every committed step leaves it clean), so
// nothing of the branch's is lost.
func restoreTree(wtPath, sha string) {
	_, _ = git.Run(wtPath, "reset", "-q", "--hard", sha)
}

// trackedChanges names the tracked paths with uncommitted changes in wtPath,
// or "" when there are none. Untracked files are not counted.
func trackedChanges(wtPath string) string {
	out, err := git.Run(wtPath, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return "could not read the worktree status: " + err.Error()
	}
	lines := nonEmptyLines(out)
	if len(lines) == 0 {
		return ""
	}
	const keep = 5
	if len(lines) > keep {
		lines = append(lines[:keep], fmt.Sprintf("... and %d more", len(lines)-keep))
	}
	return strings.Join(lines, "; ")
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func shortSha(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
