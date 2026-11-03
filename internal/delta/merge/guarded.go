package merge

import (
	"context"
	"errors"
	"io"
	"os"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/charlie/check"
)

// The guarded merge (spinclass#449). The repo's pre-merge gate runs the
// branch's own tree in the frontend (its justfile, through the session
// worktree's devshell), so for a session whose tree a confined worker
// controls it cannot be the control: the worker would be choosing what the
// operator's uid executes. A guarded merge therefore runs no repair (it runs
// in the session worktree's devshell) and, in place of the pre-merge hook, a
// command from a trusted sweatfile layer in the detached build worktree with
// no devshell and a scrubbed environment (check.RunGuardedWithReporterContext).
// The gate-definition digest rule, when it lands, stays the review gate for
// committed changes to the files the ordinary gate reads.

// guardedMerge is whether pm asks for the guard or the session behind wtPath
// was launched confined (confinement.GuardedWorktree, which fails closed).
func guardedMerge(pm PostMergeOptions, wtPath string) bool {
	return pm.GuardedGate || confinement.GuardedWorktree(wtPath)
}

// skipRepairForGuardedMerge records, when the repo configured a repair, that a
// guarded merge did not run it: repair amends HEAD in the session worktree
// under its devshell, which is exactly where a guarded merge runs nothing.
func (p mergePreamble) skipRepairForGuardedMerge(ts *crap.TestStream, branch string) {
	if p.haveHierarchy && p.hierarchy.Merged.RepairActive() {
		ts.Ok("repair " + branch + " (skipped: a guarded merge runs no repair, it would execute the branch's tree in the session worktree's devshell)")
	}
}

// runGuardedGate runs the trusted guarded gate for a merge. A hierarchy that
// did not load is a refusal, not a merge with no hook: the gate's command is
// chosen from the trusted layers, and without them nothing may land.
func runGuardedGate(ctx context.Context, rep *crap.Reporter, ts *crap.TestStream, hierarchy sweatfile.Hierarchy, ok bool, repoPath, wtPath, branch, hookSha string, activity io.Writer) ([]check.BlobLink, error) {
	if !ok {
		return nil, failStep(ts, "guarded gate "+branch, errors.New("the trusted sweatfile hierarchy did not load (home unresolvable, or the default branch's sweatfile unreadable), so no guarded gate could be chosen; nothing landed"), "")
	}
	home, _ := os.UserHomeDir()
	gate, _ := sweatfileio.TrustedConfinedPreMerge(hierarchy, home, repoPath)
	return check.RunGuardedWithReporterContext(ctx, rep, ts, hierarchy, wtPath, branch, hookSha, gate, activity)
}
