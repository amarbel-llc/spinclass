package run

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
	"code.linenisgreat.com/spinclass/internal/charlie/check"
)

// regenerateCommitSubject is the one commit the phase makes.
const regenerateCommitSubject = "chore: regenerate and format under updated flake.lock"

// runRepair runs a repair-shaped hook (a command in the worktree under its
// devshell, bounded by [hooks].repair-timeout), a seam so a test needs no
// direnv.
var runRepair = hookrun.Repair

// regenerateCommand picks what regenerates the tree and in which mode.
//   - repair mode: [hooks].repair (FDR 0018), e.g. conformist --commit --amend.
//     It works on the whole tree and may amend HEAD.
//   - staged mode: [hooks].pre-commit (FDR 0019), e.g. conformist --staged. It
//     only sees STAGED files, so the phase stages the whole branch's diff first.
//
// ok is false when the repo configures neither.
func regenerateCommand(sf sweatfile.Sweatfile) (cmd string, staged, ok bool) {
	if sf.RepairActive() {
		return *sf.RepairHookCommand(), false, true
	}
	if sf.PreCommitActive() {
		return *sf.PreCommitHookCommand(), true, true
	}
	return "", false, false
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

// regenerateRun is R0d of the plan: with the step's lock admitted (the caller
// did that), run the repo's regenerating hook in the worktree and commit
// whatever it changed as ONE separate commit, so the generated churn is
// reviewable and revertable on its own and the step's commits stay as the
// script wrote them.
//
// Why running the hook here is the new-lock run: it runs in the worktree under
// its devshell (direnv, when the worktree has an .envrc), which evaluates the
// flake the worktree holds, and the admission judged the committed flake.nix and
// flake.lock at HEAD. A clean tracked tree makes the two the same tree, so a
// dirty one is refused here, as REPAIR refuses it (#345).
//
// Repair mode: the command may amend HEAD (conformist-repair does) or only edit
// files; an amend is undone with a soft reset to the pre-repair HEAD, the
// amended tree stays staged. Staged mode (a repo with only [hooks].pre-commit,
// such as ringmaster's): HEAD is soft-reset to the merge base with the landed
// branch, so the whole branch diff, flake.lock included, is staged and the
// hook's lanes fire as they would for the lock-bump commit, now under the new
// lock; then HEAD is put back and the difference is the regeneration. Either
// way the staged diff is committed under regenerateCommitSubject with hooks
// bypassed (the per-commit dispatcher would restamp with the frozen OLD
// formatter). Only tracked files are taken. Fail closed on a hook failure
// (staged mode restores the tree to the step's HEAD) or a dirty tree.
func regenerateRun(ctx context.Context, ts *crap.TestStream, sf sweatfile.Sweatfile, wtPath, baseRef string) error {
	cmd, staged, ok := regenerateCommand(sf)
	if !ok {
		ts.Skip(regenerateStepName, "neither [hooks].repair nor [hooks].pre-commit is configured, so there is nothing to run under the new lock")
		return nil
	}
	if dirty := trackedChanges(wtPath); dirty != "" {
		return check.FailStep(ts, regenerateStepName,
			fmt.Errorf("the worktree has uncommitted tracked changes after the step, so the tree is not the one the lock policy admitted: %s", dirty),
			"commit or discard them in the step, or pass --no-regenerate")
	}
	sha0, err := git.RevParse(wtPath, "HEAD")
	if err != nil {
		return check.FailStep(ts, regenerateStepName, fmt.Errorf("could not resolve HEAD: %w", err), "")
	}
	if staged {
		mb, mbErr := git.Run(wtPath, "merge-base", "HEAD", baseRef)
		mb = strings.TrimSpace(mb)
		if mbErr != nil || mb == "" {
			return check.FailStep(ts, regenerateStepName, fmt.Errorf("no merge base between HEAD and %s, so the branch's diff cannot be staged for the hook: %v", baseRef, mbErr), "")
		}
		if mb != sha0 {
			if _, rErr := git.Run(wtPath, "reset", "-q", "--soft", mb); rErr != nil {
				return check.FailStep(ts, regenerateStepName, fmt.Errorf("could not stage the branch's diff for the hook: %w", rErr), "")
			}
		}
	}

	var out bytes.Buffer
	if hookErr := runRepair(ctx, asRepairHook(sf, cmd), wtPath, &out); hookErr != nil {
		restoreTree(wtPath, sha0)
		return check.FailStep(ts, regenerateStepName, fmt.Errorf("the hook failed under the new lock (the worktree was put back to %s): %w", shortSha(sha0), hookErr), out.String())
	}

	sha1, err := git.RevParse(wtPath, "HEAD")
	if err != nil {
		return check.FailStep(ts, regenerateStepName, fmt.Errorf("could not resolve HEAD after the hook: %w", err), out.String())
	}
	switch {
	case sha1 != sha0:
		// An amend, or staged mode's moved HEAD: put HEAD back to the step's
		// commit and keep the resulting tree staged.
		if _, rErr := git.Run(wtPath, "reset", "-q", "--soft", sha0); rErr != nil {
			return check.FailStep(ts, regenerateStepName,
				fmt.Errorf("could not split the hook's result from the step's commit (HEAD is %s, expected %s): %w", sha1, sha0, rErr), out.String())
		}
	case !staged:
		if _, aErr := git.Run(wtPath, "add", "-u"); aErr != nil {
			return check.FailStep(ts, regenerateStepName, fmt.Errorf("could not stage the hook's edits: %w", aErr), out.String())
		}
	}
	names, nErr := git.Run(wtPath, "diff", "--cached", "--name-only")
	if nErr != nil {
		return check.FailStep(ts, regenerateStepName, fmt.Errorf("could not list the hook's changes: %w", nErr), out.String())
	}
	changed := nonEmptyLines(names)
	if len(changed) == 0 {
		ts.Ok(regenerateStepName + " (already conformant under the new lock)")
		return nil
	}
	if _, cErr := git.Run(wtPath, "commit", "-q", "--no-verify", "-m", regenerateCommitSubject); cErr != nil {
		return check.FailStep(ts, regenerateStepName, fmt.Errorf("could not commit the regenerated files: %w", cErr), out.String())
	}
	sha2, _ := git.RevParse(wtPath, "HEAD")
	ts.Ok(fmt.Sprintf("%s (committed %s: %d file(s))", regenerateStepName, shortSha(sha2), len(changed)))
	return nil
}

// restoreTree puts the worktree back at sha, discarding what a failed hook left
// in the index and tree. It is only called on a tree that was clean at sha
// (regenerateRun refuses a dirty one first), so nothing of the step's is lost.
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
