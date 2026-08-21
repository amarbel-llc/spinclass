package merge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"

	"code.linenisgreat.com/spinclass/internal/check"
	"code.linenisgreat.com/spinclass/internal/executor"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/mergelock"
	"code.linenisgreat.com/spinclass/internal/present"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// mergeInteractive reports whether both stdin and stderr are TTYs. huh renders
// via stderr (tea.WithOutput(os.Stderr)), so both fds must be terminals before
// we invoke any interactive prompt. Overridable in tests.
var mergeInteractive = func() bool {
	stdin := os.Stdin.Fd()
	stderr := os.Stderr.Fd()
	return (isatty.IsTerminal(stdin) || isatty.IsCygwinTerminal(stdin)) &&
		(isatty.IsTerminal(stderr) || isatty.IsCygwinTerminal(stderr))
}

func Run(execr executor.Executor, format string, target string, gitSync bool, postMergeTargets []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	resolved, err := present.ResolveFormat(format, isatty.IsTerminal(os.Stdout.Fd()))
	if err != nil {
		return err
	}

	// Implicit (main-checkout) session with no explicit target: hook-then-push,
	// no rebase. Only a live implicit session at cwd routes here; otherwise we
	// fall through to the normal worktree/target resolution below. The CLI is
	// gate-free by design (see attestation.go package doc) — no attestation here.
	if target == "" && !worktree.IsWorktree(cwd) {
		if implicit, _, ferr := session.FindImplicitAtCwd(cwd); ferr == nil && implicit != nil {
			return present.WithReporter(resolved, "merge "+implicit.Branch, os.Stdout, os.Stderr, func(rep *crap.Reporter) error {
				ts := rep.TestStream(0)
				defer ts.Finish()
				_, mergeErr := MergeImplicit(context.Background(), rep, ts, implicit.RepoPath, cwd, implicit.Branch, nil, postMergeTargets)
				return mergeErr
			})
		}
	}

	var repoPath, wtPath, branch string
	inSession := false

	switch {
	case worktree.IsWorktree(cwd) && target == "":
		repoPath, err = git.CommonDir(cwd)
		if err != nil {
			return fmt.Errorf("not in a worktree directory: %s", cwd)
		}
		wtPath = cwd
		branch, err = git.BranchCurrent(cwd)
		if err != nil {
			return fmt.Errorf("could not determine current branch: %w", err)
		}
		inSession = isInsideSession(cwd, wtPath)
	case target != "":
		repoPath, wtPath, branch, err = resolveTarget(cwd, target)
		if err != nil {
			return err
		}
	default:
		if worktree.IsWorktree(cwd) {
			repoPath, err = git.CommonDir(cwd)
		} else {
			repoPath, err = worktree.DetectRepo(cwd)
		}
		if err != nil {
			return fmt.Errorf("not in a git repository: %s", cwd)
		}

		wtPath, branch, err = chooseWorktree(repoPath)
		if err != nil {
			return err
		}
	}

	// Resolve the default branch BEFORE entering the reporter scope:
	// ResolveDefaultBranch may huh-prompt, and a TUI cannot nest inside the
	// live viewport renderer.
	defaultBranch, err := ResolveDefaultBranch(repoPath)
	if err != nil {
		return err
	}

	return present.WithReporter(resolved, "merge "+branch, os.Stdout, os.Stderr, func(rep *crap.Reporter) error {
		ts := rep.TestStream(0)
		defer ts.Finish()
		_, mergeErr := Resolved(execr, rep, ts, repoPath, wtPath, branch, defaultBranch, gitSync, inSession, postMergeTargets)
		return mergeErr
	})
}

// Resolved orchestrates the rebase/pre-merge-hook/merge sequence for a
// fully-resolved worktree, emitting one ndjson-crap test point per stage
// onto the caller's shared TestStream (the caller owns ts.Finish()).
// Returns any resource_link blobs emitted by the pre-merge hook (one per
// hook step that produced a madder blob; empty when madder is not pinned
// at build time) and a non-nil error if any step failed. Each BlobLink
// carries the MIME type matching the format the blob was written in.
func Resolved(execr executor.Executor, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch string, gitSync, inSession bool, postMergeTargets []string) ([]check.BlobLink, error) {
	return ResolvedContext(context.Background(), execr, rep, ts, repoPath, wtPath, branch, defaultBranch, gitSync, inSession, nil, postMergeTargets)
}

// ResolvedContext is Resolved bound to ctx with an optional activity writer.
// ctx threads to the pre-merge hook subprocess (cancellable by the async job
// runner); activity, when non-nil, is teed the hook's live output (the async
// job log). Synchronous callers use Resolved (background ctx, nil activity).
//
// defaultBranch must be non-empty: resolving it may huh-prompt
// (ResolveDefaultBranch), which cannot run inside the reporter scope, so
// callers resolve it before building the Reporter.
//
// postMergeTargets selects which named [[post-merge]] targets deploy (FDR
// 0026): nil = all active targets (the default), a non-nil list = exactly those
// names (empty = none). An unknown name fails PrepareMerge before anything
// lands.
func ResolvedContext(ctx context.Context, execr executor.Executor, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch string, gitSync, inSession bool, activity io.Writer, postMergeTargets []string) ([]check.BlobLink, error) {
	if info, statErr := os.Stat(repoPath); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("repository not found: %s", repoPath)
	}

	if defaultBranch == "" {
		return nil, errors.New("default branch not resolved: callers must resolve it (ResolveDefaultBranch) before ResolvedContext")
	}

	pinnedSha, prepErr := PrepareMerge(ts, repoPath, wtPath, branch, defaultBranch, gitSync, postMergeTargets)
	if prepErr != nil {
		return nil, prepErr
	}

	return FinishMerge(ctx, execr, rep, ts, repoPath, wtPath, branch, defaultBranch, pinnedSha, gitSync, inSession, activity, postMergeTargets)
}

// PrepareMerge runs the fast, session-worktree-touching prefix of a merge: the
// disable-merge gate, optional pull of defaultBranch, rebase of branch onto it,
// and the nothing-to-merge short-circuit. On success it returns the pinned
// post-rebase HEAD sha — the exact commit FinishMerge verifies and merges, so a
// commit landing on branch after PrepareMerge returns does not change what gets
// merged. Stages emit test points on ts; PrepareMerge never finishes the
// stream — the caller owns ts.Finish().
//
// Splitting prepare from finish lets the async merge tool run this prefix
// synchronously (before returning the job id), freeing the session worktree the
// moment the rebase lands while FinishMerge's slow pre-merge hook runs detached
// in an isolated build worktree.
func PrepareMerge(ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch string, gitSync bool, postMergeTargets []string) (pinnedSha string, err error) {
	emitCoActiveSessions(ts, repoPath, wtPath)

	// Load the sweatfile hierarchy once for the disable-merge gate and the
	// repair phase. An unresolvable home or load failure degrades gracefully:
	// both the gate and repair are skipped rather than blocking the merge.
	var (
		hierarchy     sweatfile.Hierarchy
		haveHierarchy bool
	)
	if home, _ := os.UserHomeDir(); home != "" {
		if h, hErr := sweatfileio.LoadWorktreeHierarchy(home, repoPath, wtPath); hErr == nil {
			hierarchy, haveHierarchy = h, true
		}
	}

	if haveHierarchy && hierarchy.Merged.DisableMergeEnabled() {
		disableErr := fmt.Errorf(
			"merge disabled by sweatfile (disable-merge=true at %s); use `sc check` to run the pre-merge hook without merging",
			disableMergeSource(hierarchy),
		)
		return "", failStep(ts, "merge "+branch, disableErr, "")
	}

	// Validate the post-merge target selection BEFORE anything lands (FDR 0026):
	// a caller naming a target no [[post-merge]] stanza declares is a typo that
	// would otherwise silently skip the deploy the caller intended, so it is the
	// one post-merge concern that can still be fatal — nothing has shipped yet.
	// A nil selection (deploy all) needs no validation; an empty one (deploy
	// none) is always valid.
	if postMergeTargets != nil {
		var active []sweatfile.PostMergeTarget
		if haveHierarchy {
			active = hierarchy.Merged.ActivePostMergeTargets()
		}
		if _, selErr := selectPostMergeTargets(active, postMergeTargets); selErr != nil {
			return "", failStep(ts, "post-merge selection "+branch, selErr, "")
		}
	}

	// Pull the default branch BEFORE rebasing, so the session branch is
	// rebased onto the current origin tip rather than a stale local ref.
	// Otherwise a concurrent commit on origin/<default> arriving between
	// session start and merge leaves `git merge --ff-only` unable to
	// fast-forward. See #29.
	if gitSync {
		out, pullErr := git.Pull(repoPath)
		if pullErr != nil {
			return "", failStep(ts, "pull "+defaultBranch, pullErr, out)
		}
		ts.Ok("pull " + defaultBranch)
	}

	out, rebaseErr := git.RunEnv(wtPath, []string{"GIT_SEQUENCE_EDITOR=true"}, "rebase", defaultBranch, "-i")
	if rebaseErr != nil {
		return "", failStep(ts, "rebase "+branch, rebaseErr, out)
	}
	ts.Ok("rebase " + branch)

	// A rebase that exits 0 can still leave the worktree conflicted: a failed
	// autostash pop (git config rebase.autoStash) applies the rebase cleanly but
	// leaves unmerged paths + conflict markers in the worktree — a failed pop is
	// a warning, not a rebase failure. Halt HERE, before the repair phase:
	// otherwise `[hooks].repair` (conformist --commit --amend) would stage and
	// amend those markers into the rebased commit, landing a non-building,
	// conflict-marked commit in history that only the pre-merge build catches
	// (#200). The markers stay in the worktree (uncommitted) for the agent to
	// resolve, then re-merge.
	if conflicted, cErr := git.UnmergedPaths(wtPath); cErr != nil {
		return "", failStep(ts, "conflict check "+branch, cErr, "")
	} else if len(conflicted) > 0 {
		conflictErr := fmt.Errorf(
			"unresolved conflicts after rebase (resolve them in the worktree, then re-merge): %s",
			strings.Join(conflicted, ", "),
		)
		return "", failStep(ts, "conflict check "+branch, conflictErr, "")
	}

	// Short-circuit so an empty merge doesn't pay for the pre-merge hook.
	if git.CommitsAhead(wtPath, defaultBranch, branch) == 0 {
		noopErr := fmt.Errorf("nothing to merge: %s has no commits ahead of %s", branch, defaultBranch)
		return "", failStep(ts, "merge "+branch, noopErr, "")
	}

	// REPAIR phase (FDR 0018): auto-fold mechanical fixes into the commit being
	// merged before the VERIFY hook. Runs here — after the nothing-to-merge guard
	// (so HEAD is an unpushed session commit conformist's --amend accepts) and
	// before the pin (so the pin reads the post-repair HEAD). Skipped when repair
	// is inactive or the hierarchy did not load.
	if haveHierarchy {
		if rErr := runRepairPhase(ts, hierarchy, wtPath, branch); rErr != nil {
			return "", rErr
		}
	}

	// Pin the post-rebase (and post-repair) tip: FinishMerge verifies and merges
	// exactly this sha, so work committed onto branch while the hook runs is left
	// for a later merge.
	pinnedSha, shaErr := git.RevParse(wtPath, "HEAD")
	if shaErr != nil {
		return "", failStep(ts, "merge "+branch, fmt.Errorf("could not resolve %s HEAD: %w", branch, shaErr), "")
	}
	return pinnedSha, nil
}

// ErrIntegrationConflict is returned when the merge queue's landing rebase —
// replaying the pinned session commits onto a default branch that moved while
// this merge waited for the lock — hits conflicts. It is the ONLY hard failure
// class introduced by the merge queue (spinclass#235). Resolution: re-merge,
// which rebases the session worktree onto the moved tip so the conflicts can
// be resolved there.
var ErrIntegrationConflict = errors.New("integration conflict with commits that landed during the merge gate")

// LandWorktreePrefix is the filename prefix of a transient landing-rebase
// worktree under <repo>/.worktrees/: ".land-<branch>-<shortsha>-<pid>"
// (mirrors check.BuildWorktreePrefix's naming convention).
const LandWorktreePrefix = ".land-"

// FinishMerge runs the slow, committing suffix of a merge against pinnedSha
// (the sha PrepareMerge returned).
//
// By default (spinclass#235) it serializes on the per-repo landing lock
// (internal/mergelock, an flock in the shared .git dir) and, under the lock:
// re-pulls the default branch (gitSync only), checks whether the default-branch
// tip is still an ancestor of pinnedSha, rebases the pinned commits onto a
// moved tip in a transient landing worktree when it is not, runs the pre-merge
// hook against the exact LANDING sha, ff-only merges it, tears down, and
// pushes — so the gate always verifies the tree that actually lands and the
// ff-only merge can no longer lose a race to a concurrent merge. Teardown is
// guarded by the pin contract: when the session branch tip advanced past
// pinnedSha while this merge waited (commits "left for a later merge"), the
// worktree and branch are KEPT — not removed/deleted — so the post-pin commits
// stay reachable for a follow-up merge; the push still happens.
//
// With [hooks].disable-merge-queue the pre-#235 path runs instead: hook on
// pinnedSha → ff-only → teardown → push, no lock.
//
// The pre-merge hook runs in an isolated detached build worktree pinned to the
// hook sha unless [hooks].disable-merge-build-worktree is set. Stages emit
// test points on ts; FinishMerge never finishes the stream — the caller owns
// ts.Finish().
func FinishMerge(ctx context.Context, execr executor.Executor, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch, pinnedSha string, gitSync, inSession bool, activity io.Writer, postMergeTargets []string) (blobLinks []check.BlobLink, err error) {
	_ = execr // kept for signature stability; close requests go through executor.RequestClose

	// Merge-queue knob. Mirrors PrepareMerge's graceful-degrade hierarchy
	// load: an unresolvable home or a load failure leaves the queue ENABLED —
	// the knob only disables when explicitly readable as true.
	queueDisabled := false
	if home, _ := os.UserHomeDir(); home != "" {
		if h, hErr := sweatfileio.LoadWorktreeHierarchy(home, repoPath, wtPath); hErr == nil {
			queueDisabled = h.Merged.DisableMergeQueueEnabled()
		}
	}
	if queueDisabled {
		return finishMergeUnqueued(ctx, rep, ts, repoPath, wtPath, branch, defaultBranch, pinnedSha, gitSync, inSession, activity, postMergeTargets)
	}

	// Acquire the per-repo landing lock BEFORE the gate, so the gate always
	// runs under the lock against the exact tree that lands (the load-bearing
	// change of spinclass#235). The lock file lives inside the shared .git
	// dir — git.CommonGitDir, NOT git.CommonDir (which is the main-checkout
	// ROOT) — so it never appears in worktree status.
	lockDir, lockDirErr := git.CommonGitDir(repoPath)
	if lockDirErr != nil {
		return nil, failStep(ts, "merge queue "+branch, fmt.Errorf("resolve git common dir for merge lock: %w", lockDirErr), "")
	}
	holderID := filepath.Base(repoPath) + "/" + branch // the session key `sc list` prints

	// Periodic wait heartbeats go only to activity (the async job log): test
	// points are one-shot, so the stream instead gets a single post-acquire
	// summary point. The [hooks].inactivity-timeout watchdog wraps only the
	// hook subprocess (sweatfile.RunPreMergeHookInDir), so time spent queued
	// here is naturally exempt from it.
	var (
		waited     bool
		lastHolder string
	)
	waitStart := time.Now()
	lock, lockErr := mergelock.Acquire(ctx, lockDir, holderID, func(holder string, elapsed time.Duration) {
		waited = true
		if holder == "" {
			holder = "another session"
		}
		lastHolder = holder
		if activity != nil {
			_, _ = fmt.Fprintf(activity, "merge queue: waiting behind %s (%s)\n", holder, elapsed.Round(time.Second))
		}
	})
	if lockErr != nil {
		return nil, failStep(ts, "merge queue "+branch, lockErr, "")
	}
	// Release is idempotent, so the deferred release covers every early-return
	// error path; the push below still happens under the lock.
	defer func() { _ = lock.Release() }()
	if waited {
		ts.Ok(fmt.Sprintf("merge queue wait %s (behind %s)", time.Since(waitStart).Round(time.Second), lastHolder))
	}

	// (a) Re-pull under the lock: PrepareMerge's pull is now stale by the
	// length of the queue wait.
	if gitSync {
		out, pullErr := git.Pull(repoPath)
		if pullErr != nil {
			return nil, failStep(ts, "pull "+defaultBranch+" (landing)", pullErr, out)
		}
		ts.Ok("pull " + defaultBranch + " (landing)")
	}

	// (b) Ancestry check: pinnedSha lands as-is iff the default-branch tip is
	// still an ancestor of it (nothing landed since PrepareMerge pinned).
	landingSha := pinnedSha
	rebased := false
	cleanupLand := func() {}
	if !git.IsAncestor(repoPath, defaultBranch, pinnedSha) {
		// (c) The branch lost the race: rebase the pinned commits onto the
		// moved tip in a transient detached worktree — NOT the session
		// worktree, whose HEAD may have advanced past the pin (that is the
		// pin contract).
		var landErr error
		landingSha, cleanupLand, landErr = rebaseLanding(ts, repoPath, branch, defaultBranch, pinnedSha)
		if landErr != nil {
			return nil, landErr
		}
		rebased = true
	}
	// Deferred safety net for error paths below; the explicit call after the
	// ff is the intended removal point. Idempotent.
	defer cleanupLand()

	// (d) The gate, under the lock, against the exact sha that will land.
	// (With [hooks].disable-merge-build-worktree the hook runs in the session
	// worktree instead — pre-existing resolveHookDir behavior, in which the
	// sha it verifies is whatever that worktree has checked out.)
	hookLinks, hookErr := runPreMergeHookContext(ctx, rep, ts, repoPath, wtPath, branch, landingSha, activity)
	blobLinks = append(blobLinks, hookLinks...)
	if hookErr != nil {
		return blobLinks, hookErr
	}

	// (e) ff-only merge of the landing sha, with a distinct label when the
	// landing was rebased past a moved tip.
	mergeLabel := "merge " + branch
	if rebased {
		mergeLabel = "merge " + branch + " (rebased onto moved " + defaultBranch + ")"
	}
	out, mergeErr := git.Run(repoPath, "merge", "--ff-only", landingSha)
	if mergeErr != nil {
		return blobLinks, failStep(ts, mergeLabel, mergeErr, out)
	}
	ts.Ok(mergeLabel)

	// The landing commit is now reachable from defaultBranch; until here the
	// transient landing worktree's HEAD was its only ref.
	cleanupLand()

	// (f)+(g) teardown and push, still under the lock. The pin contract allows
	// commits to land on branch after PrepareMerge pins ("left for a later
	// merge"), and the queue wait + gate make that window long: resolve the
	// branch's CURRENT tip (from repoPath — worktree-independent) and only
	// allow teardown when it still equals the pin. A RevParse failure
	// conservatively reads as NOT matching — skip deletion rather than risk
	// force-deleting unreachable post-pin commits.
	tip, tipErr := git.RevParse(repoPath, "refs/heads/"+branch)
	tipMatchesPin := tipErr == nil && tip == pinnedSha
	if tdErr := teardownAndPush(ts, repoPath, wtPath, branch, gitSync, inSession, rebased, tipMatchesPin); tdErr != nil {
		return blobLinks, tdErr
	}

	// (h) The post-merge hook runs UNDER the landing lock, like every other
	// stage of the merge. The queue's contract (FDR 0022) is that a merge is
	// exclusive end to end: while the lock is held no other session may
	// perform ANY part of a merge. A post-merge deploy is part of the merge —
	// letting a sibling session land (and deploy) while this session's deploy
	// is still running would defeat the point of serializing in the first
	// place, since the two deploys could interleave and the older one could
	// win. The deferred Release above fires when FinishMerge returns.
	runPostMergePhase(ctx, rep, ts, repoPath, wtPath, branch, defaultBranch, landingSha, gitSync, activity, postMergeTargets)
	return blobLinks, nil
}

// finishMergeUnqueued is the pre-#235 FinishMerge path, kept verbatim behind
// the [hooks].disable-merge-queue rollback knob: hook on pinnedSha → ff-only →
// teardown → push, with no lock, no re-pull, and no landing rebase — a default
// branch that moved during the hook fails the ff-only merge exactly as before.
func finishMergeUnqueued(ctx context.Context, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch, pinnedSha string, gitSync, inSession bool, activity io.Writer, postMergeTargets []string) (blobLinks []check.BlobLink, err error) {
	hookLinks, hookErr := runPreMergeHookContext(ctx, rep, ts, repoPath, wtPath, branch, pinnedSha, activity)
	blobLinks = append(blobLinks, hookLinks...)
	if hookErr != nil {
		return blobLinks, hookErr
	}

	out, mergeErr := git.Run(repoPath, "merge", "--ff-only", pinnedSha)
	if mergeErr != nil {
		return blobLinks, failStep(ts, "merge "+branch, mergeErr, out)
	}
	ts.Ok("merge " + branch)

	// tipMatchesPin=true preserves the pre-#235 semantics verbatim: no tip
	// check, teardown always attempted, plain `-d` with its natural refusal
	// when the branch tip advanced past the pin.
	if tdErr := teardownAndPush(ts, repoPath, wtPath, branch, gitSync, inSession, false, true); tdErr != nil {
		return blobLinks, tdErr
	}
	// No lock on this path at all (that is what disable-merge-queue means), so
	// there is no exclusivity to preserve; pinnedSha is what landed, since the
	// unqueued path never rebases the landing.
	runPostMergePhase(ctx, rep, ts, repoPath, wtPath, branch, defaultBranch, pinnedSha, gitSync, activity, postMergeTargets)
	return blobLinks, nil
}

// rebaseLanding replays pinnedSha's commits onto the moved defaultBranch tip
// in a transient detached worktree under <repo>/.worktrees/ and returns the
// resulting landing sha plus an idempotent cleanup that force-removes the
// worktree and prunes admin entries. Call cleanup only after the ff-only merge
// — until then the landing commit's only ref is this worktree's HEAD.
//
// On rebase conflict (or any rebase failure) it best-effort aborts, removes
// the worktree, emits a failing "land <branch>" test point, and returns an
// error wrapping ErrIntegrationConflict.
func rebaseLanding(ts *crap.TestStream, repoPath, branch, defaultBranch, pinnedSha string) (landingSha string, cleanup func(), err error) {
	noop := func() {}
	landParent := filepath.Join(repoPath, ".worktrees")
	if mkErr := os.MkdirAll(landParent, 0o755); mkErr != nil {
		return "", noop, failStep(ts, "land "+branch, fmt.Errorf("create landing worktree parent %s: %w", landParent, mkErr), "")
	}
	name := LandWorktreePrefix + strings.ReplaceAll(branch, "/", "-") + "-" + shortSha(pinnedSha) + "-" + strconv.Itoa(os.Getpid())
	landPath := filepath.Join(landParent, name)

	// Clear a stale physical dir from an interrupted prior run (same guard,
	// same rationale as check.resolveHookDir).
	if rmErr := os.RemoveAll(landPath); rmErr != nil {
		return "", noop, failStep(ts, "land "+branch, fmt.Errorf("remove stale landing worktree dir %s: %w", landPath, rmErr), "")
	}
	if addErr := git.WorktreeAddDetached(repoPath, landPath, pinnedSha); addErr != nil {
		return "", noop, failStep(ts, "land "+branch, fmt.Errorf("create landing worktree at %s: %w", landPath, addErr), "")
	}
	removed := false
	cleanup = func() {
		if removed {
			return
		}
		removed = true
		_ = git.WorktreeForceRemove(repoPath, landPath)
		_ = git.WorktreePrune(repoPath)
	}

	out, rebaseErr := git.Rebase(landPath, defaultBranch)
	if rebaseErr != nil {
		conflicted, _ := git.UnmergedPaths(landPath)
		_, _ = git.Run(landPath, "rebase", "--abort") // best-effort
		cleanup()
		guidance := "commits landed during the gate conflict with this branch; re-merge to rebase and resolve in the session worktree"
		conflictErr := fmt.Errorf("%w: %s", ErrIntegrationConflict, guidance)
		if len(conflicted) > 0 {
			conflictErr = fmt.Errorf("%w (conflicting: %s): %s", ErrIntegrationConflict, strings.Join(conflicted, ", "), guidance)
		}
		return "", noop, failStep(ts, "land "+branch, conflictErr, out)
	}

	landingSha, shaErr := git.RevParse(landPath, "HEAD")
	if shaErr != nil {
		cleanup()
		return "", noop, failStep(ts, "land "+branch, fmt.Errorf("could not resolve landing HEAD: %w", shaErr), "")
	}
	return landingSha, cleanup, nil
}

// teardownAndPush is FinishMerge's shared suffix: worktree/branch teardown
// (skipped in-session or when run from inside the worktree), optional push,
// and the out-of-session close request.
//
// forceBranchDelete selects `git branch -D`: after a rebased landing the
// session branch tip is no longer an ancestor of the default branch, so `-d`
// would refuse — force is safe because the patch-identical content just landed
// via the rebased landing sha. The unrebased path keeps `-d` as the existing
// safety net.
//
// tipMatchesPin gates teardown entirely: the pin contract allows commits to
// land on branch after PrepareMerge pins, and `git worktree remove` + `-D`
// would silently strand those post-pin commits unreachable (worktree remove
// only checks dirtiness, not unmerged commits, and -D bypasses -d's ancestry
// refusal). When false, both worktree removal and branch deletion are skipped
// — the worktree and branch survive so the commits stay reachable for a later
// merge — and an ok "keep worktree" test point records why. The push still
// happens. finishMergeUnqueued passes true unconditionally (behavior-
// preserving: no tip check on the pre-#235 path).
func teardownAndPush(ts *crap.TestStream, repoPath, wtPath, branch string, gitSync, inSession, forceBranchDelete, tipMatchesPin bool) error {
	// Skip worktree removal when running from inside the worktree being
	// merged (can't remove cwd) or when inside an active session.
	insideWorktree := false
	if cwd, err := os.Getwd(); err == nil {
		insideWorktree = isInsideWorktree(cwd, wtPath)
	}

	if !inSession && !insideWorktree {
		if !tipMatchesPin {
			ts.Ok("keep worktree " + branch + " (commits added since pin; left for a later merge)")
		} else {
			out, removeErr := git.Run(repoPath, "worktree", "remove", wtPath)
			if removeErr != nil {
				return failStep(ts, "remove worktree "+branch, removeErr, out)
			}
			ts.Ok("remove worktree " + branch)

			var delErr error
			if forceBranchDelete {
				out, delErr = git.BranchForceDelete(repoPath, branch)
			} else {
				out, delErr = git.BranchDelete(repoPath, branch)
			}
			if delErr != nil {
				return failStep(ts, "delete branch "+branch, delErr, out)
			}
			ts.Ok("delete branch " + branch)
		}
	}

	if gitSync {
		out, pushErr := git.Push(repoPath)
		if pushErr != nil {
			return failStep(ts, "push", pushErr, out)
		}
		ts.Ok("push")
	}

	if inSession {
		// Session state stays put. spinclass worktrees are workers — they
		// host many sequences of work separated by `merge-this-session`,
		// and tearing down state.json + the central index symlink here
		// would orphan the worktree from `sc list`/`resume`/`close` until
		// the next session.Write. Cleanup is owned by `sc close`/`sc clean`.
		return nil
	}

	// Outside session: request graceful close if the target is still
	// running. State cleanup is delegated to the close path
	// (closeShop → close.RunResolved → session.Tombstone) when conditions
	// warrant; abandoned state is reaped by `sc clean`.
	_ = executor.RequestClose(repoPath, branch)
	return nil
}

// MergeImplicit runs the merge path for a main-checkout (implicit) session:
// the pre-merge hook against HEAD, then a push of the current (default) branch.
// There is no rebase or ff-merge — the work is already on the default branch.
// The push is surfaced as its own test point so it is never silent. Mirrors
// FinishMerge's ts/failStep idioms; the caller owns ts.Finish(). hookSha pins
// the exact committed sha the hook verifies.
//
// For an implicit session repoPath == checkout == the main checkout (they are
// the same dir): the hook runs with wtPath=checkout and the push is from
// checkout. Both params are kept for clarity and signature symmetry with
// FinishMerge even though they are equal.
//
// Unlike PrepareMerge/FinishMerge there is no gitSync parameter — push is
// unconditional. For an implicit session the work is already on the default
// branch, so there is nothing to conditionally sync; the push is always
// performed.
//
// The hook's isolated build worktree lands under <repo>/.worktrees/ even for
// a main checkout — check.resolveHookDir derives the parent from
// git.CommonDir(wtPath), not filepath.Dir(wtPath) (#130).
func MergeImplicit(ctx context.Context, rep *crap.Reporter, ts *crap.TestStream, repoPath, checkout, branch string, activity io.Writer, postMergeTargets []string) (blobLinks []check.BlobLink, err error) {
	emitCoActiveSessions(ts, repoPath, checkout)

	var (
		hierarchy     sweatfile.Hierarchy
		haveHierarchy bool
	)
	if home, _ := os.UserHomeDir(); home != "" {
		if h, hErr := sweatfileio.LoadWorktreeHierarchy(home, repoPath, checkout); hErr == nil {
			hierarchy, haveHierarchy = h, true
		}
	}
	if haveHierarchy && hierarchy.Merged.DisableMergeEnabled() {
		disableErr := fmt.Errorf(
			"merge disabled by sweatfile (disable-merge=true at %s); use `sc check` to run the pre-merge hook without merging",
			disableMergeSource(hierarchy),
		)
		return nil, failStep(ts, "merge "+branch, disableErr, "")
	}

	// Validate the post-merge target selection before anything is pushed (FDR
	// 0026), same as PrepareMerge on the worktree path.
	if postMergeTargets != nil {
		var active []sweatfile.PostMergeTarget
		if haveHierarchy {
			active = hierarchy.Merged.ActivePostMergeTargets()
		}
		if _, selErr := selectPostMergeTargets(active, postMergeTargets); selErr != nil {
			return nil, failStep(ts, "post-merge selection "+branch, selErr, "")
		}
	}

	// Pin HEAD; the hook verifies exactly this committed sha.
	pinnedSha, shaErr := git.RevParse(checkout, "HEAD")
	if shaErr != nil {
		return nil, failStep(ts, "merge "+branch, fmt.Errorf("could not resolve HEAD: %w", shaErr), "")
	}

	// Pre-merge hook (isolated build worktree pinned to pinnedSha).
	hookLinks, hookErr := runPreMergeHookContext(ctx, rep, ts, repoPath, checkout, branch, pinnedSha, activity)
	blobLinks = append(blobLinks, hookLinks...)
	if hookErr != nil {
		return blobLinks, hookErr
	}

	// Push the default branch — outward-facing, so it's a distinct test point.
	out, pushErr := git.Push(checkout)
	if pushErr != nil {
		return blobLinks, failStep(ts, "push "+branch, pushErr, out)
	}
	ts.Ok("push " + branch)

	// Post-merge hook (FDR 0023). An implicit session's work is already ON the
	// default branch, so the branch that merged and the branch it landed on are
	// the same ref, and the push is unconditional — hence pushed=true. No lock
	// is involved: implicit merges are out of the merge queue's scope entirely
	// (FDR 0022 Limitations), so this hook carries no exclusivity guarantee.
	runPostMergePhase(ctx, rep, ts, repoPath, checkout, branch, branch, pinnedSha, true, activity, postMergeTargets)
	return blobLinks, nil
}

// isInsideSession returns true when both SPINCLASS_SESSION_ID is set and cwd is
// within the worktree directory. Both checks are required to avoid false
// positives from stale env vars or running merge from a different location.
func isInsideSession(cwd, wtPath string) bool {
	session := os.Getenv("SPINCLASS_SESSION_ID")
	if session == "" {
		return false
	}

	cleanCwd := filepath.Clean(cwd)
	cleanWt := filepath.Clean(wtPath)

	return cleanCwd == cleanWt || strings.HasPrefix(cleanCwd, cleanWt+string(filepath.Separator))
}

// isInsideWorktree returns true when cwd is within the worktree directory.
func isInsideWorktree(cwd, wtPath string) bool {
	cleanCwd := filepath.Clean(cwd)
	cleanWt := filepath.Clean(wtPath)
	return cleanCwd == cleanWt || strings.HasPrefix(cleanCwd, cleanWt+string(filepath.Separator))
}

func ResolveWorktree(repoPath, target string) (wtPath, branch string, err error) {
	paths := worktree.ListWorktrees(repoPath)
	for _, p := range paths {
		if filepath.Base(p) == target {
			return p, target, nil
		}
	}
	return "", "", fmt.Errorf("worktree not found: %s", target)
}

// resolveTarget resolves an explicit merge target. The current repo's
// git worktrees match first (by dirname — bare worktrees without
// session state keep working, and a local name is never shadowed by
// another repo's session); otherwise the target resolves as a session
// target — a worktree dirname or a `<repo>/<branch>` session key as
// printed by `sc list` — which makes cross-repo merges work from any
// cwd, including outside a repo entirely.
func resolveTarget(cwd, target string) (repoPath, wtPath, branch string, err error) {
	if worktree.IsWorktree(cwd) {
		repoPath, err = git.CommonDir(cwd)
	} else {
		repoPath, err = worktree.DetectRepo(cwd)
	}
	if err == nil {
		if wtPath, branch, werr := ResolveWorktree(repoPath, target); werr == nil {
			return repoPath, wtPath, branch, nil
		}
	}

	s, serr := session.FindByTarget(target)
	if errors.Is(serr, session.ErrTargetNotFound) {
		return "", "", "", fmt.Errorf("worktree not found: %s", target)
	}
	if serr != nil {
		// Ambiguity (or index read failure): the error already carries
		// the disambiguating session keys.
		return "", "", "", serr
	}
	return s.RepoPath, s.WorktreePath, s.Branch, nil
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

	if len(paths) == 1 {
		return paths[0], branches[0], nil
	}

	if !mergeInteractive() {
		return "", "", fmt.Errorf("sc merge requires an interactive terminal to select a worktree; specify a target with `sc merge <branch>`")
	}

	var selected string
	options := make([]huh.Option[string], len(branches))
	for i, b := range branches {
		options[i] = huh.NewOption(b, b)
	}

	err = huh.NewSelect[string]().
		Title("Select worktree to merge").
		Options(options...).
		Value(&selected).
		Run()
	if err != nil {
		return "", "", fmt.Errorf("worktree selection cancelled")
	}

	for i, b := range branches {
		if b == selected {
			return paths[i], b, nil
		}
	}

	return "", "", fmt.Errorf("selected worktree not found: %s", selected)
}

func ResolveDefaultBranch(repoPath string) (string, error) {
	branch, err := git.DefaultBranch(repoPath)
	if errors.Is(err, git.ErrAmbiguousDefaultBranch) {
		return promptDefaultBranch()
	}
	if err != nil {
		return "", fmt.Errorf("could not determine default branch: %w", err)
	}
	return branch, nil
}

func promptDefaultBranch() (string, error) {
	if !mergeInteractive() {
		return "", fmt.Errorf("both main and master exist; pass default_branch='main' or default_branch='master' to the merge tool, or run sc merge interactively to select")
	}
	var selected string
	err := huh.NewSelect[string]().
		Title("Both main and master branches exist. Which should be the rebase target?").
		Options(
			huh.NewOption("main", "main"),
			huh.NewOption("master", "master"),
		).
		Value(&selected).
		Run()
	if err != nil {
		return "", fmt.Errorf("branch selection cancelled: %w", err)
	}
	return selected, nil
}

// runPreMergeHookContext loads the sweatfile hierarchy and runs the configured
// pre-merge hook via check.RunWithReporterContext on the caller's shared
// Reporter/TestStream (the merge orchestrator owns ts.Finish(); check only
// emits the hook test point — and emits nothing at all when no hook is
// configured). Returns (nil, nil) silently when home is not resolvable or the
// hierarchy fails to load. Returned BlobLinks are the resource_link blobs
// emitted for hook output.
func runPreMergeHookContext(ctx context.Context, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, hookSha string, activity io.Writer) ([]check.BlobLink, error) {
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil, nil
	}
	hierarchy, err := sweatfileio.LoadWorktreeHierarchy(home, repoPath, wtPath)
	if err != nil {
		return nil, nil
	}
	return check.RunWithReporterContext(ctx, rep, ts, hierarchy, wtPath, branch, hookSha, activity)
}

// runPostMergePhase runs the post-merge phase after a merge has landed (FDR
// 0023, extended by FDR 0026 named targets), emitting one test point per target
// on ts. It is deliberately NON-FATAL: the merge already landed (and, with
// gitSync, was pushed), so there is nothing to roll back and nothing for a
// caller to retry. A failing target emits a not-ok point carrying severity=warn,
// its verdict, and its output, but runPostMergePhase always returns — surfaced
// and logged, not treated as a merge failure (spinclass#244). Returns
// immediately when the phase is inactive or the hierarchy cannot be loaded.
//
// On the queued path this runs UNDER the per-repo merge lock, as the last
// stage before FinishMerge returns and the deferred Release fires. That is
// deliberate: FDR 0022's queue makes a merge exclusive end to end, and a
// post-merge deploy is part of the merge. Two sessions deploying concurrently
// — or a sibling landing mid-deploy — is exactly the interleaving the queue
// exists to prevent. The cost is real and accepted: a slow post-merge phase
// extends the exclusive region, so N racing sessions drain in
// N × (gate + phase) time.
//
// The phase runs in the session worktree when it still exists — teardown may
// have removed it — and otherwise in repoPath, which is sitting on the merged
// tip of the default branch either way. landedSha is the sha that actually
// landed: the LANDING sha on a rebased queued landing, not the original pin.
//
// FDR 0026: when the sweatfile declares active [[post-merge]] targets they ARE
// the phase — the legacy [hooks].post-merge string is superseded. Targets run
// sequentially in declaration order, filtered by postMergeTargets (nil = all,
// non-nil = that subset, empty = none), each with its own verdict point, all
// sharing one wall-clock deadline. With no named targets, the legacy string
// runs exactly as FDR 0023 shipped it — but only under the default (nil)
// selection, since an explicit selection names entries the unnamed string is
// not among.
func runPostMergePhase(ctx context.Context, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch, landedSha string, pushed bool, activity io.Writer, postMergeTargets []string) {
	home, _ := os.UserHomeDir()
	if home == "" {
		return
	}
	hierarchy, err := sweatfileio.LoadWorktreeHierarchy(home, repoPath, wtPath)
	if err != nil || !hierarchy.Merged.PostMergePhaseActive() {
		return
	}

	// Teardown may have removed the session worktree; repoPath is always
	// present and is on the merged default-branch tip.
	runDir := wtPath
	if info, statErr := os.Stat(runDir); statErr != nil || !info.IsDir() {
		runDir = repoPath
	}

	pushedVal := "0"
	if pushed {
		pushedVal = "1"
	}
	env := []string{
		"SPINCLASS_MERGED_SHA=" + landedSha,
		"SPINCLASS_MERGED_BRANCH=" + branch,
		"SPINCLASS_DEFAULT_BRANCH=" + defaultBranch,
		"SPINCLASS_MERGE_PUSHED=" + pushedVal,
		"SPINCLASS_REPO_PATH=" + repoPath,
	}

	// Named targets supersede the legacy string (FDR 0026).
	if active := hierarchy.Merged.ActivePostMergeTargets(); len(active) > 0 {
		runNamedPostMergeTargets(ctx, rep, hierarchy.Merged, active, postMergeTargets, runDir, env, landedSha, activity)
		return
	}

	// Legacy single-string path (FDR 0023). The unnamed string is not among any
	// explicit selection, so only the default (nil) selection runs it.
	if postMergeTargets != nil {
		return
	}

	var out bytes.Buffer
	var sink io.Writer = &out
	if activity != nil {
		sink = io.MultiWriter(&out, activity)
	}
	label := "post-merge " + branch + " (" + shortSha(landedSha) + ")"
	if hookErr := hierarchy.Merged.RunPostMergeHookContext(ctx, runDir, env, sink); hookErr != nil {
		diag := map[string]any{
			"severity": "warn",
			"message": fmt.Sprintf(
				"post-merge hook failed: %v (the merge already landed — nothing was rolled back)",
				hookErr,
			),
		}
		if o := out.String(); o != "" {
			diag["output"] = o
		}
		ts.NotOk(label, diag)
		return
	}
	ts.Ok(label)
}

// runNamedPostMergeTargets runs the selected [[post-merge]] targets (FDR 0026)
// CONCURRENTLY (spinclass#276) as execution-family Phase nodes on the reporter —
// crap's muxing model, the same one the pre-merge hook uses: each target is its
// own node with a unique id, its live output streamed as Output records tagged
// with that id, and its verdict carried on the node_end. crap's ndjson writer
// serializes the wire, so concurrent output neither races nor tears and the
// viewport demuxes each target's output under its own node — no hand-rolled
// prefixing. The nodes' node_start records are emitted up front in DECLARATION
// order (a deterministic ladder, and the reporter's unsynchronized counter is
// never raced); the goroutines then only stream output and close their own node.
//
// All targets and their verifies share ONE wall-clock deadline derived from
// post-merge-timeout, so the phase holds the merge lock for MAX(target
// durations), not their sum; when it fires, every still-running target is killed
// and its node reports verdict=timeout. Every failure is non-fatal — the merge
// already landed, so a failed node carries severity=warn and the merge still
// returns success (the "post-merge " label prefix is load-bearing so the async
// completion wake surfaces every failed target, spinclass#259). Targets are
// independent by construction (they observe no ordering between each other),
// which is what makes the fan-out safe.
func runNamedPostMergeTargets(ctx context.Context, rep *crap.Reporter, sf sweatfile.Sweatfile, active []sweatfile.PostMergeTarget, requested []string, runDir string, env []string, landedSha string, activity io.Writer) {
	selected, selErr := selectPostMergeTargets(active, requested)
	if selErr != nil {
		// Pre-landing validation (PrepareMerge/MergeImplicit) should have caught
		// this; surface defensively rather than silently deploying nothing.
		ph := rep.Phase("post-merge selection (" + shortSha(landedSha) + ")")
		ph.FailDiag(selErr, map[string]any{"severity": "warn"})
		return
	}

	// One shared wall-clock deadline for the whole phase (FDR 0026): with targets
	// running concurrently (spinclass#276), this bounds lock-hold to the slowest
	// single target rather than their sum. PostMergeTimeoutValue is 10m by
	// default; <=0 means the cap is disabled.
	phaseCtx := ctx
	phaseCap := sf.PostMergeTimeoutValue()
	if phaseCap > 0 {
		var cancel context.CancelFunc
		phaseCtx, cancel = context.WithTimeout(ctx, phaseCap)
		defer cancel()
	}

	// Allocate every node up front, single-threaded: node_starts land in
	// declaration order and the reporter's unsynchronized counter is never
	// raced. Each goroutine below touches only its own already-allocated node.
	phases := make([]*crap.Phase, len(selected))
	for i, tgt := range selected {
		ph := rep.Phase("post-merge " + tgt.Name + " (" + shortSha(landedSha) + ")")
		ph.Command(tgt.Command)
		phases[i] = ph
	}

	// repMu guards the reporter's unsynchronized state (its sticky err field)
	// across the concurrent nodes' Output/close writes, and serializes the shared
	// raw activity tee (the async job log). crap's ndjson writer is already
	// line-atomic; this is the wrapper-level guard the Reporter itself lacks.
	var repMu sync.Mutex
	var wg sync.WaitGroup
	for i := range selected {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tgt := selected[i]
			ph := phases[i]
			lw := present.NewLineWriter(ph)
			sink := &postMergeTargetSink{mu: &repMu, lw: lw, activity: activity}

			verdict, runErr := tgt.Run(phaseCtx, runDir, env, sink)
			// Snapshot the deadline/cancel state now, before a sibling's later
			// kill can move phaseCtx.Err out from under a genuine failure.
			timedOut := runErr != nil && errors.Is(phaseCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
			cancelled := runErr != nil && ctx.Err() != nil

			repMu.Lock()
			defer repMu.Unlock()
			lw.Flush()
			if runErr == nil {
				ph.Done()
				return
			}
			ph.FailDiag(nil, postMergeFailDiag(tgt, verdict, runErr, timedOut, cancelled, phaseCap))
		}(i)
	}
	wg.Wait()
}

// postMergeFailDiag builds the node_end diagnostic for a failed post-merge target
// (FDR 0026): severity=warn (a landed merge is never a failure), the verdict
// (command-failed / verify-failed / timeout / cancelled), the stage it failed at,
// and an operator-facing message. The target's output is NOT duplicated here — it
// already streamed as Output records on the node.
func postMergeFailDiag(tgt sweatfile.PostMergeTarget, verdict sweatfile.PostMergeVerdict, runErr error, timedOut, cancelled bool, phaseCap time.Duration) map[string]any {
	stage := "command"
	if verdict == sweatfile.PostMergeVerifyFailed {
		stage = "verify"
	}
	diag := map[string]any{"severity": "warn", "verdict": string(verdict), "stage": stage}
	switch {
	// Our shared deadline killed it (only when the caller's ctx was still live —
	// otherwise the kill is the caller's cancel, below).
	case timedOut:
		diag["verdict"] = "timeout"
		diag["message"] = fmt.Sprintf(
			"post-merge target %q killed at the %s stage: the phase exceeded post-merge-timeout %s "+
				"(the merge already landed — nothing was rolled back; raise [hooks].post-merge-timeout, or 0 to disable)",
			tgt.Name, stage, phaseCap,
		)
	case cancelled:
		diag["verdict"] = "cancelled"
		diag["message"] = fmt.Sprintf("post-merge target %q cancelled at the %s stage: %v", tgt.Name, stage, runErr)
	case verdict == sweatfile.PostMergeVerifyFailed:
		diag["message"] = fmt.Sprintf(
			"post-merge target %q deployed but verify failed: %v (the merge already landed — nothing was rolled back)",
			tgt.Name, runErr,
		)
	default:
		diag["message"] = fmt.Sprintf(
			"post-merge target %q command failed: %v (the merge already landed — nothing was rolled back)",
			tgt.Name, runErr,
		)
	}
	return diag
}

// postMergeTargetSink streams one concurrent post-merge target's output to its
// reporter node (via lw, which forwards complete lines as Output records) and,
// when non-nil, tees the raw bytes to the async job log. One mutex — shared
// across every target's sink — guards both, so the reporter's unsynchronized
// state and the shared activity writer stay race-free; a single lock per Write
// keeps lw's node output and the activity tee from interleaving mid-call.
type postMergeTargetSink struct {
	mu       *sync.Mutex
	lw       *present.LineWriter // wraps this target's Phase; used only under mu
	activity io.Writer           // raw job-log tee; may be nil
}

func (s *postMergeTargetSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activity != nil {
		_, _ = s.activity.Write(p)
	}
	return s.lw.Write(p)
}

// selectPostMergeTargets resolves a caller's requested post-merge selection (FDR
// 0026) against the active targets: nil selects all (declaration order), a
// non-nil list selects exactly those names (also declaration order), empty
// selects none. It errors — naming the unknown names and the declared set — if
// any requested name is not an active target, which PrepareMerge turns into a
// fatal pre-landing failure so a typo never silently skips a deploy.
func selectPostMergeTargets(active []sweatfile.PostMergeTarget, requested []string) ([]sweatfile.PostMergeTarget, error) {
	if requested == nil {
		return active, nil
	}
	declared := make(map[string]bool, len(active))
	for _, t := range active {
		declared[t.Name] = true
	}
	want := make(map[string]bool, len(requested))
	var unknown []string
	for _, name := range requested {
		if !declared[name] {
			unknown = append(unknown, name)
		}
		want[name] = true
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf(
			"unknown post-merge target(s): %s (declared: %s)",
			strings.Join(unknown, ", "), postMergeTargetNames(active),
		)
	}
	var out []sweatfile.PostMergeTarget
	for _, t := range active {
		if want[t.Name] {
			out = append(out, t)
		}
	}
	return out, nil
}

func postMergeTargetNames(targets []sweatfile.PostMergeTarget) string {
	if len(targets) == 0 {
		return "(none)"
	}
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

// runRepairPhase runs the [hooks].repair command (FDR 0018) in wtPath when
// active, emitting a test point on ts, and aborts the merge on a nonzero exit.
// On success the HEAD-sha delta distinguishes an amend ("amended <sha>") from a
// no-op ("already conformant"). Returns nil (no-op) when repair is inactive.
//
// Repair deliberately runs in the session worktree, not the build worktree: the
// build worktree's pinned-sha `merge --ff-only` would discard an amend made
// there, and threading the repaired sha out would strand the session branch and
// replay-conflict on the next merge (see FDR 0018). It uses context.Background
// because PrepareMerge is the synchronous prefix — for async merge it runs
// before the cancellable job exists, and repair is a fast formatter pass.
func runRepairPhase(ts *crap.TestStream, hierarchy sweatfile.Hierarchy, wtPath, branch string) error {
	if !hierarchy.Merged.RepairActive() {
		return nil
	}

	// Pre-repair HEAD; the delta against the post-repair HEAD is the
	// tool-agnostic "did it amend" signal (the repair command exits 0 whether or
	// not it changed anything, e.g. conformist --exit-zero-on-fix).
	sha0, _ := git.RevParse(wtPath, "HEAD")

	var out bytes.Buffer
	if hookErr := hierarchy.Merged.RunRepairHookContext(context.Background(), wtPath, &out); hookErr != nil {
		return failStep(ts, "repair "+branch, fmt.Errorf("repair hook failed: %w", hookErr), out.String())
	}

	sha1, shaErr := git.RevParse(wtPath, "HEAD")
	if shaErr != nil {
		return failStep(ts, "repair "+branch, fmt.Errorf("could not resolve HEAD after repair: %w", shaErr), out.String())
	}
	if sha1 == sha0 {
		ts.Ok("repair " + branch + " (already conformant)")
		return nil
	}
	ts.Ok("repair " + branch + " (amended " + shortSha(sha1) + ")")
	return nil
}

// emitCoActiveSessions emits one informational ok test point listing the OTHER
// active sessions on the repo when a merge starts (spinclass#238), e.g.
// "2 co-active sessions on <repo>: bright-cherry, bright-olive". The session
// being merged is excluded by its worktree path; for an implicit merge that
// path is the checkout, which also excludes indistinguishable implicit
// siblings. Best-effort: a listing failure emits nothing and never fails the
// merge. Both merge entry points (PrepareMerge and MergeImplicit) call this;
// `sc check` / the check tools do not, so a check run stays silent.
func emitCoActiveSessions(ts *crap.TestStream, repoPath, excludeWorktree string) {
	others, err := session.ListActiveForRepoExcluding(repoPath, excludeWorktree)
	if err != nil || len(others) == 0 {
		return
	}
	names := make([]string, len(others))
	for i := range others {
		names[i] = others[i].BranchOrKey()
	}
	ts.Ok(session.CoActiveSummary("co-active", filepath.Base(repoPath), names))
}

// shortSha truncates a git object id to 12 chars for display, leaving shorter
// ids untouched.
func shortSha(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// failStep emits a failing test point for label populated from err
// (severity=fail), including the step's captured output when non-empty.
// Never finishes ts — the merge orchestrator owns stream termination so
// exactly one summary is emitted per run. Returns err unchanged so callers
// can write `return failStep(...)`.
func failStep(ts *crap.TestStream, label string, err error, output string) error {
	diag := map[string]any{"severity": "fail", "message": err.Error()}
	if output != "" {
		diag["output"] = output
	}
	ts.NotOk(label, diag)
	return err
}

// disableMergeSource returns the path of the most-specific sweatfile in
// the hierarchy that set DisableMerge to true, or "<unknown>"
// if none can be located.
func disableMergeSource(h sweatfile.Hierarchy) string {
	for i := len(h.Sources) - 1; i >= 0; i-- {
		s := h.Sources[i]
		if !s.Found {
			continue
		}
		if s.File.Hooks != nil && s.File.Hooks.DisableMerge != nil && *s.File.Hooks.DisableMerge {
			return s.Path
		}
	}
	return "<unknown>"
}
