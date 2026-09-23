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

	"code.linenisgreat.com/spinclass/internal/auth"
	"code.linenisgreat.com/spinclass/internal/check"
	"code.linenisgreat.com/spinclass/internal/executor"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/hookrun"
	"code.linenisgreat.com/spinclass/internal/landing"
	"code.linenisgreat.com/spinclass/internal/mergelock"
	"code.linenisgreat.com/spinclass/internal/present"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/statsd"
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

func Run(execr executor.Executor, format string, target string, gitSync bool, pm PostMergeOptions) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	resolved, err := present.ResolveFormat(format, isatty.IsTerminal(os.Stdout.Fd()))
	if err != nil {
		return err
	}

	// A live implicit (main-checkout) session at cwd with no explicit target is
	// refused (#317): merging from a main checkout is unsupported. Without an
	// implicit session we fall through to the normal worktree/target resolution.
	if target == "" && !worktree.IsWorktree(cwd) {
		if implicit, _, ferr := session.FindImplicitAtCwd(cwd); ferr == nil && implicit != nil {
			return ErrImplicitMergeUnsupported
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
		_, mergeErr := Resolved(execr, rep, ts, repoPath, wtPath, branch, defaultBranch, gitSync, inSession, pm)
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
func Resolved(execr executor.Executor, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch string, gitSync, inSession bool, pm PostMergeOptions) ([]check.BlobLink, error) {
	return ResolvedContext(context.Background(), execr, rep, ts, repoPath, wtPath, branch, defaultBranch, gitSync, inSession, nil, pm)
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
// pm carries the per-merge post-merge configuration (PostMergeOptions): the
// named-target selection (FDR 0026: nil = all, a non-nil list = exactly those
// names, empty = none; an unknown name fails PrepareMerge before anything
// lands) and the optional post-merge-timeout override.
func ResolvedContext(ctx context.Context, execr executor.Executor, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch string, gitSync, inSession bool, activity io.Writer, pm PostMergeOptions) ([]check.BlobLink, error) {
	if info, statErr := os.Stat(repoPath); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("repository not found: %s", repoPath)
	}

	if defaultBranch == "" {
		return nil, errors.New("default branch not resolved: callers must resolve it (ResolveDefaultBranch) before ResolvedContext")
	}

	pinnedSha, prepErr := PrepareMerge(ts, repoPath, wtPath, branch, defaultBranch, gitSync, pm)
	if prepErr != nil {
		return nil, prepErr
	}

	return FinishMerge(ctx, execr, rep, ts, repoPath, wtPath, branch, defaultBranch, pinnedSha, gitSync, inSession, activity, pm)
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
func PrepareMerge(ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch string, gitSync bool, pm PostMergeOptions) (pinnedSha string, err error) {
	preamble, gateErr := loadAndGate(ts, repoPath, wtPath, branch, pm.Targets)
	if gateErr != nil {
		return "", gateErr
	}

	// Rebase onto the landing target (#315): a gitSync merge's target is the
	// remote's default branch, freshly fetched (#29) — the root's local default
	// ref is never read, so a stale, diverged, or dirty-blocked local branch
	// cannot fail or skew the merge. A local-only merge targets the local
	// branch itself.
	target := landing.ForMerge(repoPath, defaultBranch, gitSync)
	if err := fetchTarget(context.Background(), ts, target, wtPath, ""); err != nil {
		return "", err
	}

	out, rebaseErr := git.RunEnv(wtPath, []string{"GIT_SEQUENCE_EDITOR=true"}, "rebase", target.Ref(), "-i")
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
	if git.CommitsAhead(wtPath, target.Ref(), branch) == 0 {
		noopErr := fmt.Errorf("nothing to merge: %s has no commits ahead of %s", branch, target.Label())
		return "", failStep(ts, "merge "+branch, noopErr, "")
	}

	// REPAIR phase (FDR 0018): auto-fold mechanical fixes into the commit being
	// merged before the VERIFY hook. Runs here — after the nothing-to-merge guard
	// (so HEAD is an unpushed session commit conformist's --amend accepts) and
	// before the pin (so the pin reads the post-repair HEAD). The rebase and that
	// guard already establish repair's preconditions, so no skip check.
	if rErr := preamble.repair(ts, wtPath, branch); rErr != nil {
		return "", rErr
	}

	// Pin the post-rebase (and post-repair) tip: FinishMerge verifies and merges
	// exactly this sha, so work committed onto branch while the hook runs is left
	// for a later merge.
	return pinHead(ts, wtPath, branch)
}

// mergePreamble carries the sweatfile hierarchy loaded by loadAndGate into
// PrepareMerge's later phases.
type mergePreamble struct {
	hierarchy     sweatfile.Hierarchy
	haveHierarchy bool
}

// loadAndGate is the head of a merge: the co-active sessions point, the
// sweatfile hierarchy load for dir, the disable-merge gate, and the post-merge
// target validation. An unresolvable home or load failure degrades gracefully —
// the gate and the later repair phase are skipped rather than blocking the
// merge.
func loadAndGate(ts *crap.TestStream, repoPath, dir, branch string, postMergeTargets []string) (mergePreamble, error) {
	emitCoActiveSessions(ts, repoPath, dir)

	var p mergePreamble
	if home, _ := os.UserHomeDir(); home != "" {
		if h, hErr := sweatfileio.LoadWorktreeHierarchy(home, repoPath, dir); hErr == nil {
			p.hierarchy, p.haveHierarchy = h, true
		}
	}

	if p.haveHierarchy && p.hierarchy.Merged.DisableMergeEnabled() {
		disableErr := fmt.Errorf(
			"merge disabled by sweatfile (disable-merge=true at %s); use `sc check` to run the pre-merge hook without merging",
			disableMergeSource(p.hierarchy),
		)
		return p, failStep(ts, "merge "+branch, disableErr, "")
	}

	// Validate the post-merge target selection BEFORE anything lands (FDR 0026):
	// a caller naming a target no [[post-merge]] stanza declares is a typo that
	// would otherwise silently skip the deploy the caller intended, so it is the
	// one post-merge concern that can still be fatal — nothing has shipped yet.
	// A nil selection (deploy all) needs no validation; an empty one (deploy
	// none) is always valid.
	if postMergeTargets != nil {
		var active []sweatfile.PostMergeTarget
		if p.haveHierarchy {
			active = p.hierarchy.Merged.ActivePostMergeTargets()
		}
		if _, selErr := selectPostMergeTargets(active, postMergeTargets); selErr != nil {
			return p, failStep(ts, "post-merge selection "+branch, selErr, "")
		}
	}
	return p, nil
}

// repair runs the REPAIR phase (FDR 0018) in dir. It is a no-op when the
// hierarchy did not load or repair is inactive.
func (p mergePreamble) repair(ts *crap.TestStream, dir, branch string) error {
	if !p.haveHierarchy || !p.hierarchy.Merged.RepairActive() {
		return nil
	}
	return runRepairPhase(ts, p.hierarchy, dir, branch)
}

// pinHead resolves dir's HEAD as the sha the pre-merge hook verifies and the
// landing publishes, emitting a failing point on error.
func pinHead(ts *crap.TestStream, dir, branch string) (string, error) {
	sha, err := git.RevParse(dir, "HEAD")
	if err != nil {
		return "", failStep(ts, "merge "+branch, fmt.Errorf("could not resolve %s HEAD: %w", branch, err), "")
	}
	return sha, nil
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
// re-fetches the landing target (gitSync only), checks whether the target tip
// is still an ancestor of pinnedSha, rebases the pinned commits onto a moved
// tip in a transient landing worktree when it is not, runs the pre-merge hook
// against the exact LANDING sha, lands it, and tears down — so the gate always
// verifies the tree that actually lands and the landing can no longer lose a
// race to a concurrent merge.
//
// The landing itself (spinclass#284, Alt B; landing.Target, #315): a gitSync
// merge pushes the landing sha straight to the remote's default branch from a
// disposable detached landing worktree, and a refused push (stale tip, dropped
// credential) exits having moved nothing, so a failed landing is a plain retry.
// The root checkout's local default branch is never read for correctness. A
// local-only merge (gitSync=false) fast-forwards the local default branch:
// there, the local ref IS the landing.
//
// Teardown is guarded by the pin contract: when the session branch tip advanced
// past pinnedSha while this merge waited (commits "left for a later merge"),
// the worktree and branch are KEPT — not removed/deleted — so the post-pin
// commits stay reachable for a follow-up merge; the landing still happens.
//
// With [hooks].disable-merge-queue the pre-#235 path runs instead: hook on
// pinnedSha → ff-only into the root → teardown → push from the root, no lock
// (and none of the Alt B landing).
//
// The pre-merge hook runs in an isolated detached build worktree pinned to the
// hook sha unless [hooks].disable-merge-build-worktree is set. Stages emit
// test points on ts; FinishMerge never finishes the stream — the caller owns
// ts.Finish().
func FinishMerge(ctx context.Context, execr executor.Executor, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch, pinnedSha string, gitSync, inSession bool, activity io.Writer, pm PostMergeOptions) (blobLinks []check.BlobLink, err error) {
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
		return finishMergeUnqueued(ctx, rep, ts, repoPath, wtPath, branch, defaultBranch, pinnedSha, gitSync, inSession, activity, pm)
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
	// hook subprocess (hookrun.PreMergeInDir), so time spent queued
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

	// (a) Re-fetch under the lock: PrepareMerge's fetch is now stale by the
	// length of the queue wait.
	target := landing.ForMerge(repoPath, defaultBranch, gitSync)
	if fErr := fetchTarget(ctx, ts, target, wtPath, " (landing)"); fErr != nil {
		return nil, fErr
	}

	// (b) Ancestry check: pinnedSha lands as-is iff the target tip is still an
	// ancestor of it (nothing landed since PrepareMerge pinned).
	needRebase := !git.IsAncestor(repoPath, target.Ref(), pinnedSha)

	// (c) The disposable landing worktree (#284, Alt B). A gitSync merge always
	// lands from one: the landing sha is pushed to the remote from that detached
	// worktree, so no local ref moves and the worktree carries whatever
	// worktree-scoped git config a per-session push credential needs (FDR
	// 0028). A local-only merge needs it only when the branch lost the race and
	// the pinned commits must be rebased onto the moved tip — NOT in the session
	// worktree, whose HEAD may have advanced past the pin (the pin contract).
	landingSha := pinnedSha
	rebased := false
	landPath := ""
	cleanupLand := func() {}
	if gitSync || needRebase {
		var landErr error
		landPath, cleanupLand, landErr = addLandingWorktree(ts, repoPath, branch, pinnedSha)
		if landErr != nil {
			return nil, landErr
		}
	}
	// Idempotent; the safety net for every error path below. The worktree is
	// kept through the post-merge phase, which runs in it.
	defer cleanupLand()
	// The landing worktree pushes, so it needs the session's per-session push
	// credential wiring (FDR 0028) — a no-op when the session minted none.
	if gitSync {
		if mErr := auth.MirrorInto(wtPath, landPath); mErr != nil {
			return nil, failStep(ts, "land "+branch, fmt.Errorf("mirror push credential into landing worktree: %w", mErr), "")
		}
	}
	if needRebase {
		var landErr error
		landingSha, landErr = rebaseLanding(ts, landPath, branch, target.Ref())
		if landErr != nil {
			return nil, landErr
		}
		rebased = true
	}

	// (d) The gate, under the lock, against the exact sha that will land.
	// (With [hooks].disable-merge-build-worktree the hook runs in the session
	// worktree instead — pre-existing resolveHookDir behavior, in which the
	// sha it verifies is whatever that worktree has checked out.)
	hookLinks, hookErr := runPreMergeHookContext(ctx, rep, ts, repoPath, wtPath, branch, landingSha, activity)
	blobLinks = append(blobLinks, hookLinks...)
	if hookErr != nil {
		return blobLinks, hookErr
	}

	// (e) Land the landing sha, with a distinct label when the landing was
	// rebased past a moved tip. gitSync: push it to the remote's default branch
	// from the landing worktree — the ff check is the remote's own, so a stale
	// or unauthenticated push fails having moved NOTHING, local or remote. Local
	// only: the fast-forward of the local default branch is the landing.
	mergeLabel := "merge " + branch
	if rebased {
		mergeLabel = "merge " + branch + " (rebased onto moved " + defaultBranch + ")"
	}
	if out, landErr := target.Land(landPath, landingSha); landErr != nil {
		if !target.IsSelf() {
			statsd.Count(MetricPushRefused)
		}
		return blobLinks, failStep(ts, mergeLabel, landErr, out)
	}
	ts.Ok(mergeLabel)
	if !target.IsSelf() {
		statsd.Count(MetricLanded)
	}

	// (e') Advance the root's local default branch to what just landed, still
	// under the lock (a sibling's landing moves the same ref) and before the
	// post-merge phase (so deploy scripts see it). Ergonomics only (#295): a
	// refusal is a SKIP, never a failure — the merge has already landed.
	reportLocalAdvance(ts, target, landingSha)

	// (f) Teardown, still under the lock; nothing is pushed here — a gitSync
	// landing already pushed in (e). The pin contract allows commits to land on
	// branch after PrepareMerge pins ("left for a later merge"), and the queue
	// wait + gate make that window long: resolve the branch's CURRENT tip (from
	// repoPath — worktree-independent) and only allow teardown when it still
	// equals the pin. A RevParse failure conservatively reads as NOT matching —
	// skip deletion rather than risk force-deleting unreachable post-pin
	// commits.
	tip, tipErr := git.RevParse(repoPath, "refs/heads/"+branch)
	tipMatchesPin := tipErr == nil && tip == pinnedSha
	// Always force: `-d` asks whether the branch is merged into the ROOT's
	// HEAD, which need not be the default branch at all (a push landing may
	// leave local behind; a self landing fast-forwards master even while the
	// root is parked elsewhere). Force is safe because teardown only runs when
	// tipMatchesPin — the tip IS what just landed.
	if tdErr := teardownAndPush(ts, repoPath, wtPath, branch, false, inSession, true, tipMatchesPin); tdErr != nil {
		return blobLinks, tdErr
	}

	// (g) The post-merge hook runs UNDER the landing lock, like every other
	// stage of the merge. The queue's contract (FDR 0022) is that a merge is
	// exclusive end to end: while the lock is held no other session may
	// perform ANY part of a merge. A post-merge deploy is part of the merge —
	// letting a sibling session land (and deploy) while this session's deploy
	// is still running would defeat the point of serializing in the first
	// place, since the two deploys could interleave and the older one could
	// win. The deferred Release above fires when FinishMerge returns. It runs in
	// the landing worktree — the exact tree that landed.
	runPostMergePhase(ctx, rep, ts, repoPath, wtPath, landPath, branch, defaultBranch, pinnedSha, landingSha, gitSync, activity, pm)
	return blobLinks, nil
}

// finishMergeUnqueued is the pre-#235 FinishMerge path, kept verbatim behind
// the [hooks].disable-merge-queue rollback knob: hook on pinnedSha → ff-only →
// teardown → push, with no lock, no re-pull, and no landing rebase — a default
// branch that moved during the hook fails the ff-only merge exactly as before.
func finishMergeUnqueued(ctx context.Context, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch, pinnedSha string, gitSync, inSession bool, activity io.Writer, pm PostMergeOptions) (blobLinks []check.BlobLink, err error) {
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
	runPostMergePhase(ctx, rep, ts, repoPath, wtPath, "", branch, defaultBranch, pinnedSha, pinnedSha, gitSync, activity, pm)
	return blobLinks, nil
}

// fetchTarget refreshes a remote target's tracking ref, emitting a
// "fetch <remote>/<branch><suffix>" point; a no-op (no point) for self. It
// fetches from credDir — the session worktree, the one place a per-session
// push credential (FDR 0028) is wired, so a dropped ssh-agent cannot fail it.
// It deliberately does NOT touch the root's local default branch (#315): the
// merge reads only the tracking ref, and the local branch is advanced once,
// opportunistically, after the landing push.
func fetchTarget(ctx context.Context, ts *crap.TestStream, target landing.Target, credDir, suffix string) error {
	if target.IsSelf() {
		return nil
	}
	label := "fetch " + target.Label() + suffix
	if out, err := target.Fetch(ctx, credDir); err != nil {
		return failStep(ts, label, err, out)
	}
	ts.Ok(label)
	return nil
}

// Merge-landing counters (spinclass#314), emitted only on the queued remote
// landing path, so local_advance.ok + local_advance.skip == landed holds by
// construction — the invariant FDR 0029's promotion criterion checks. The
// per-cause skip counter is a sibling name (skip_reason.<slug>), not a child
// of skip, so graphite never has to hold skip as both a leaf and a branch.
const (
	MetricLanded                 = "merge.landed"
	MetricPushRefused            = "merge.push_refused"
	MetricLocalAdvanceOk         = "merge.local_advance.ok"
	MetricLocalAdvanceSkip       = "merge.local_advance.skip"
	MetricLocalAdvanceSkipReason = "merge.local_advance.skip_reason."
)

// LocalAdvanceSkipPrefix opens every local-advance skip reason, so a reader —
// and the async completion wake, which lifts these lines — sees first that
// the merge DID land (#295).
const LocalAdvanceSkipPrefix = "merge LANDED on "

// reportLocalAdvance opportunistically fast-forwards a remote target's local
// default branch to landingSha and emits an "advance local <branch>" point: ok
// when it moved (or already matched), a skip naming the landing, the reason,
// and the reconcile command otherwise. A no-op for self, whose landing already
// moved the local branch.
func reportLocalAdvance(ts *crap.TestStream, target landing.Target, landingSha string) {
	if target.IsSelf() {
		return
	}
	label := "advance local " + target.Branch
	adv := target.AdvanceLocal(landingSha)
	if !adv.Skipped() {
		statsd.Count(MetricLocalAdvanceOk)
		ts.Ok(label + " to " + shortSha(landingSha))
		return
	}
	statsd.Count(MetricLocalAdvanceSkip)
	statsd.Count(MetricLocalAdvanceSkipReason + adv.SkipSlug())
	ts.Skip(label, fmt.Sprintf("%s%s at %s; only local %s was not advanced: %s",
		LocalAdvanceSkipPrefix, target.Label(), shortSha(landingSha), target.Branch, adv.SkipReason()))
}

// addLandingWorktree creates the transient detached landing worktree
// (.land-<branch>-<shortsha>-<pid> under <repo>/.worktrees/) checked out at
// pinnedSha and returns its path plus an idempotent cleanup that force-removes
// it and prunes admin entries. A failure emits a failing "land <branch>" test
// point.
func addLandingWorktree(ts *crap.TestStream, repoPath, branch, pinnedSha string) (landPath string, cleanup func(), err error) {
	noop := func() {}
	landParent := filepath.Join(repoPath, ".worktrees")
	if mkErr := os.MkdirAll(landParent, 0o755); mkErr != nil {
		return "", noop, failStep(ts, "land "+branch, fmt.Errorf("create landing worktree parent %s: %w", landParent, mkErr), "")
	}
	name := LandWorktreePrefix + strings.ReplaceAll(branch, "/", "-") + "-" + shortSha(pinnedSha) + "-" + strconv.Itoa(os.Getpid())
	landPath = filepath.Join(landParent, name)

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
	return landPath, cleanup, nil
}

// rebaseLanding replays the landing worktree's HEAD (the pinned commits) onto
// the moved landing-target tip (targetRef, landing.Target.Ref) and returns the
// resulting landing sha. Until that sha lands, the worktree's HEAD is its only
// ref — the caller owns the worktree's cleanup and must not run it before then.
//
// On rebase conflict (or any rebase failure) it best-effort aborts, emits a
// failing "land <branch>" test point, and returns an error wrapping
// ErrIntegrationConflict.
func rebaseLanding(ts *crap.TestStream, landPath, branch, targetRef string) (landingSha string, err error) {
	out, rebaseErr := git.Rebase(landPath, targetRef)
	if rebaseErr != nil {
		conflicted, _ := git.UnmergedPaths(landPath)
		_, _ = git.Run(landPath, "rebase", "--abort") // best-effort
		guidance := "commits landed during the gate conflict with this branch; re-merge to rebase and resolve in the session worktree"
		conflictErr := fmt.Errorf("%w: %s", ErrIntegrationConflict, guidance)
		if len(conflicted) > 0 {
			conflictErr = fmt.Errorf("%w (conflicting: %s): %s", ErrIntegrationConflict, strings.Join(conflicted, ", "), guidance)
		}
		return "", failStep(ts, "land "+branch, conflictErr, out)
	}

	landingSha, shaErr := git.RevParse(landPath, "HEAD")
	if shaErr != nil {
		return "", failStep(ts, "land "+branch, fmt.Errorf("could not resolve landing HEAD: %w", shaErr), "")
	}
	return landingSha, nil
}

// teardownAndPush is FinishMerge's shared suffix: worktree/branch teardown
// (skipped in-session or when run from inside the worktree), an optional push
// of the root checkout's default branch, and the out-of-session close request.
//
// push selects the legacy post-teardown `git push` from the root checkout. The
// queued path passes false: its landing is itself the push (#284, Alt B), and
// there is nothing in the root to push. finishMergeUnqueued passes gitSync.
//
// forceBranchDelete selects `git branch -D`. The queued path always forces
// (its `-d` would ask about the root's HEAD, not the landing; tipMatchesPin is
// the real guard). finishMergeUnqueued keeps `-d` as its pre-#235 safety net.
//
// tipMatchesPin gates teardown entirely: the pin contract allows commits to
// land on branch after PrepareMerge pins, and `git worktree remove` + `-D`
// would silently strand those post-pin commits unreachable (worktree remove
// only checks dirtiness, not unmerged commits, and -D bypasses -d's ancestry
// refusal). When false, both worktree removal and branch deletion are skipped
// — the worktree and branch survive so the commits stay reachable for a later
// merge — and an ok "keep worktree" test point records why. The landing has
// already happened (queued path) or the push still does (unqueued).
// finishMergeUnqueued passes true unconditionally (behavior-preserving: no tip
// check on the pre-#235 path).
func teardownAndPush(ts *crap.TestStream, repoPath, wtPath, branch string, push, inSession, forceBranchDelete, tipMatchesPin bool) error {
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
			// The worktree is about to go, and with it the session's credential
			// file and state — revoke the per-session forge token first (FDR
			// 0028), or an out-of-session merge / `sc run` teardown would orphan
			// it to the issuer's TTL sweep (no tombstone is written here for the
			// orphan sweep to find). Non-fatal, like close and clean.
			revokeCredential(ts, repoPath, wtPath, branch)

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

	if push {
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

// revokeCredential runs [auth].revoke-command for a session that minted a
// credential (FDR 0028), as one result-family test point on ts, before the
// merge's teardown removes its worktree. Mirrors close.revokeCredential: a
// failure is a severity=warn point, never a merge failure — the merge has
// landed, and the issuer's TTL sweep is the backstop for the token.
func revokeCredential(ts *crap.TestStream, repoPath, wtPath, branch string) {
	if !auth.Minted(wtPath) {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	h, err := sweatfileio.LoadWorktreeHierarchy(home, repoPath, wtPath)
	if err != nil {
		return
	}
	sessionKey := filepath.Base(repoPath) + "/" + branch
	if st, rerr := session.Read(repoPath, branch); rerr == nil && st.SessionKey != "" {
		sessionKey = st.SessionKey
	}
	var out bytes.Buffer
	id := auth.Identity{RepoPath: repoPath, WorktreePath: wtPath, Branch: branch, SessionKey: sessionKey}
	ran, rerr := auth.Revoke(context.Background(), h.Merged, id, &out)
	if !ran {
		return
	}
	desc := "revoke credential " + branch
	if rerr != nil {
		diag := map[string]any{
			"severity": "warn",
			"message":  fmt.Sprintf("%v (the merge landed; the token is left for the issuer's sweep)", rerr),
		}
		if o := strings.TrimSpace(out.String()); o != "" {
			diag["output"] = o
		}
		ts.NotOk(desc, diag)
		return
	}
	ts.Ok(desc)
}

// ErrImplicitMergeUnsupported refuses a merge from a repo's main checkout
// (an implicit session, FDR 0014). That path was a second landing pipeline
// without the worktree path's guarantees (merge lock, landing target,
// exact-sha push) and was removed (#317); bootstrapping a main checkout as a
// real session is explored in #318. `sc check` / check-this-session still gate
// a main checkout.
var ErrImplicitMergeUnsupported = errors.New(
	"merging from a repo's main checkout is not supported (spinclass#317): " +
		"start a worktree session (`sc start`, or spawn-session) and merge from there; " +
		"`sc check` / check-this-session still run the pre-merge gate here. " +
		"Bootstrapping a main checkout as a mergeable session: spinclass#318",
)

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
// The phase runs in the landing worktree (landDir) when the merge has one —
// the exact tree that landed (#284) — otherwise in the session worktree when it
// still exists (teardown may have removed it), and otherwise in repoPath.
// landedSha is the sha that actually landed: the LANDING sha on a rebased
// queued landing, not the original pin.
//
// FDR 0026: when the sweatfile declares active [[post-merge]] targets they ARE
// the phase — the legacy [hooks].post-merge string is superseded. Targets run
// concurrently, filtered by pm.Targets (nil = all, non-nil = that subset,
// empty = none), each with its own verdict node, all sharing one wall-clock
// deadline. With no named targets, the legacy string runs exactly as FDR 0023
// shipped it — but only under the default (nil) selection, since an explicit
// selection names entries the unnamed string is not among.
//
// The cap is resolved ONCE here (pm.EffectiveTimeout: the per-merge override,
// else the sweatfile, else the default) and both enforced and advertised from
// that one value: every hook sees it as SPINCLASS_POST_MERGE_TIMEOUT(_SECONDS)
// and the phase's absolute SPINCLASS_POST_MERGE_DEADLINE, so a verify poll can
// size itself from the budget it actually has rather than hard-coding one.
// pinnedSha is the pre-landing pin (SPINCLASS_PINNED_SHA); it differs from
// landedSha exactly when the queued landing was rebased past a moved tip.
func runPostMergePhase(ctx context.Context, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, landDir, branch, defaultBranch, pinnedSha, landedSha string, pushed bool, activity io.Writer, pm PostMergeOptions) {
	home, _ := os.UserHomeDir()
	if home == "" {
		return
	}
	hierarchy, err := sweatfileio.LoadWorktreeHierarchy(home, repoPath, wtPath)
	if err != nil || !hierarchy.Merged.PostMergePhaseActive() {
		return
	}
	postMergeTargets := pm.Targets

	// repoPath is always present; the landing worktree and the session
	// worktree may not be (no landing worktree on the unqueued path;
	// teardown may have removed the session worktree).
	runDir := repoPath
	for _, candidate := range []string{landDir, wtPath} {
		if candidate == "" {
			continue
		}
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			runDir = candidate
			break
		}
	}

	// One deadline for the whole phase, computed before either path starts so
	// the advertised SPINCLASS_POST_MERGE_DEADLINE is the deadline that is
	// enforced (the named-target path derives its ctx from this exact instant;
	// the legacy path's inner cap starts microseconds later, so the advertised
	// value is at worst marginally conservative). <= 0 means uncapped.
	phaseCap := pm.EffectiveTimeout(hierarchy.Merged)
	var deadline time.Time
	if phaseCap > 0 {
		deadline = time.Now().Add(phaseCap)
	}
	env := PostMergeEnv(PostMergeFacts{
		LandedSha:     landedSha,
		PinnedSha:     pinnedSha,
		Branch:        branch,
		DefaultBranch: defaultBranch,
		RepoPath:      repoPath,
		Pushed:        pushed,
		Timeout:       phaseCap,
		Deadline:      deadline,
	})

	// Named targets supersede the legacy string (FDR 0026).
	if active := hierarchy.Merged.ActivePostMergeTargets(); len(active) > 0 {
		runNamedPostMergeTargets(ctx, rep, active, postMergeTargets, runDir, env, landedSha, phaseCap, deadline, activity)
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
	if hookErr := hookrun.PostMergeWithCap(ctx, hierarchy.Merged, runDir, env, phaseCap, sink); hookErr != nil {
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
//
// phaseCap/deadline are the EFFECTIVE cap runPostMergePhase resolved (per-merge
// override, else sweatfile, else default) and the absolute instant it expires —
// the same values the hooks were handed as SPINCLASS_POST_MERGE_TIMEOUT and
// SPINCLASS_POST_MERGE_DEADLINE, so what is advertised is what is enforced.
// Each target additionally gets SPINCLASS_POST_MERGE_TARGET=<its name>, so one
// script can serve several targets.
func runNamedPostMergeTargets(ctx context.Context, rep *crap.Reporter, active []sweatfile.PostMergeTarget, requested []string, runDir string, env []string, landedSha string, phaseCap time.Duration, deadline time.Time, activity io.Writer) {
	selected, selErr := selectPostMergeTargets(active, requested)
	if selErr != nil {
		// Pre-landing validation (PrepareMerge) should have caught
		// this; surface defensively rather than silently deploying nothing.
		ph := rep.Phase("post-merge selection (" + shortSha(landedSha) + ")")
		ph.FailDiag(selErr, map[string]any{"severity": "warn"})
		return
	}

	// One shared wall-clock deadline for the whole phase (FDR 0026): with targets
	// running concurrently (spinclass#276), this bounds lock-hold to the slowest
	// single target rather than their sum. <=0 means the cap is disabled.
	phaseCtx := ctx
	if phaseCap > 0 {
		var cancel context.CancelFunc
		phaseCtx, cancel = context.WithDeadline(ctx, deadline)
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

			// A fresh slice per goroutine: append onto the shared env would race
			// on its backing array when it has spare capacity.
			tgtEnv := make([]string, 0, len(env)+1)
			tgtEnv = append(tgtEnv, env...)
			tgtEnv = append(tgtEnv, "SPINCLASS_POST_MERGE_TARGET="+tgt.Name)

			verdict, runErr := hookrun.Target(phaseCtx, tgt, runDir, tgtEnv, sink)
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
				"(the merge already landed — nothing was rolled back; raise [hooks].post-merge-timeout or pass "+
				"post_merge_timeout on the merge call, or 0 to disable)",
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

// runRepairPhase runs the [hooks].repair command (FDR 0018) in wtPath, emitting
// a test point on ts, and aborts the merge on a nonzero exit. On success the
// HEAD-sha delta distinguishes an amend ("amended <sha>") from a no-op
// ("already conformant"). Callers go through mergePreamble.repair, which owns
// the active/skip decisions.
//
// Repair deliberately runs in the session worktree, not the build worktree: the
// build worktree's pinned-sha `merge --ff-only` would discard an amend made
// there, and threading the repaired sha out would strand the session branch and
// replay-conflict on the next merge (see FDR 0018). It uses context.Background
// because PrepareMerge is the synchronous prefix — for async merge it runs
// before the cancellable job exists, and repair is a fast formatter pass.
func runRepairPhase(ts *crap.TestStream, hierarchy sweatfile.Hierarchy, wtPath, branch string) error {
	// Pre-repair HEAD; the delta against the post-repair HEAD is the
	// tool-agnostic "did it amend" signal (the repair command exits 0 whether or
	// not it changed anything, e.g. conformist --exit-zero-on-fix).
	sha0, _ := git.RevParse(wtPath, "HEAD")

	var out bytes.Buffer
	if hookErr := hookrun.Repair(context.Background(), hierarchy.Merged, wtPath, &out); hookErr != nil {
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
// being merged is excluded by its worktree path. Best-effort: a listing failure
// emits nothing and never fails the merge. PrepareMerge calls this; `sc check`
// / the check tools do not, so a check run stays silent.
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
