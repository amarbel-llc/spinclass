package merge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/amarbel-llc/crap/go-crap/v2/crap"
	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"

	"github.com/amarbel-llc/spinclass/internal/check"
	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/present"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/sweatfileio"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

// mergeInteractive reports whether stdin is a TTY. Overridable in tests to
// exercise the no-TTY guards in chooseWorktree and promptDefaultBranch.
var mergeInteractive = func() bool {
	fd := os.Stdin.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

func Run(execr executor.Executor, format string, target string, gitSync bool) error {
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
				_, mergeErr := MergeImplicit(context.Background(), rep, ts, implicit.RepoPath, cwd, implicit.Branch, nil)
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
		_, mergeErr := Resolved(execr, rep, ts, repoPath, wtPath, branch, defaultBranch, gitSync, inSession)
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
func Resolved(execr executor.Executor, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch string, gitSync, inSession bool) ([]check.BlobLink, error) {
	return ResolvedContext(context.Background(), execr, rep, ts, repoPath, wtPath, branch, defaultBranch, gitSync, inSession, nil)
}

// ResolvedContext is Resolved bound to ctx with an optional activity writer.
// ctx threads to the pre-merge hook subprocess (cancellable by the async job
// runner); activity, when non-nil, is teed the hook's live output (the async
// job log). Synchronous callers use Resolved (background ctx, nil activity).
//
// defaultBranch must be non-empty: resolving it may huh-prompt
// (ResolveDefaultBranch), which cannot run inside the reporter scope, so
// callers resolve it before building the Reporter.
func ResolvedContext(ctx context.Context, execr executor.Executor, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch string, gitSync, inSession bool, activity io.Writer) ([]check.BlobLink, error) {
	if info, statErr := os.Stat(repoPath); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("repository not found: %s", repoPath)
	}

	if defaultBranch == "" {
		return nil, errors.New("default branch not resolved: callers must resolve it (ResolveDefaultBranch) before ResolvedContext")
	}

	pinnedSha, prepErr := PrepareMerge(ts, repoPath, wtPath, branch, defaultBranch, gitSync)
	if prepErr != nil {
		return nil, prepErr
	}

	return FinishMerge(ctx, execr, rep, ts, repoPath, wtPath, branch, defaultBranch, pinnedSha, gitSync, inSession, activity)
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
func PrepareMerge(ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch string, gitSync bool) (pinnedSha string, err error) {
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

// FinishMerge runs the slow, committing suffix of a merge against pinnedSha (the
// sha PrepareMerge returned): the pre-merge hook (run in an isolated detached
// build worktree pinned to pinnedSha unless [hooks].disable-merge-build-worktree
// is set), the ff-only merge of pinnedSha into defaultBranch, optional
// worktree/branch teardown, and push. Stages emit test points on ts; FinishMerge
// never finishes the stream — the caller owns ts.Finish().
func FinishMerge(ctx context.Context, execr executor.Executor, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch, pinnedSha string, gitSync, inSession bool, activity io.Writer) (blobLinks []check.BlobLink, err error) {
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

	// Skip worktree removal when running from inside the worktree being
	// merged (can't remove cwd) or when inside an active session.
	insideWorktree := false
	if cwd, err := os.Getwd(); err == nil {
		insideWorktree = isInsideWorktree(cwd, wtPath)
	}

	if !inSession && !insideWorktree {
		out, removeErr := git.Run(repoPath, "worktree", "remove", wtPath)
		if removeErr != nil {
			return blobLinks, failStep(ts, "remove worktree "+branch, removeErr, out)
		}
		ts.Ok("remove worktree " + branch)

		out, delErr := git.BranchDelete(repoPath, branch)
		if delErr != nil {
			return blobLinks, failStep(ts, "delete branch "+branch, delErr, out)
		}
		ts.Ok("delete branch " + branch)
	}

	if gitSync {
		out, pushErr := git.Push(repoPath)
		if pushErr != nil {
			return blobLinks, failStep(ts, "push", pushErr, out)
		}
		ts.Ok("push")
	}

	if inSession {
		// Session state stays put. spinclass worktrees are workers — they
		// host many sequences of work separated by `merge-this-session`,
		// and tearing down state.json + the central index symlink here
		// would orphan the worktree from `sc list`/`resume`/`close` until
		// the next session.Write. Cleanup is owned by `sc close`/`sc clean`.
		return blobLinks, nil
	}

	// Outside session: request graceful close if the target is still
	// running. State cleanup is delegated to the close path
	// (closeShop → close.RunResolved → session.Tombstone) when conditions
	// warrant; abandoned state is reaped by `sc clean`.
	_ = executor.RequestClose(repoPath, branch)
	return blobLinks, nil
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
func MergeImplicit(ctx context.Context, rep *crap.Reporter, ts *crap.TestStream, repoPath, checkout, branch string, activity io.Writer) (blobLinks []check.BlobLink, err error) {
	if home, _ := os.UserHomeDir(); home != "" {
		hierarchy, hErr := sweatfileio.LoadWorktreeHierarchy(home, repoPath, checkout)
		if hErr == nil && hierarchy.Merged.DisableMergeEnabled() {
			disableErr := fmt.Errorf(
				"merge disabled by sweatfile (disable-merge=true at %s); use `sc check` to run the pre-merge hook without merging",
				disableMergeSource(hierarchy),
			)
			return nil, failStep(ts, "merge "+branch, disableErr, "")
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
		return "", fmt.Errorf("both main and master exist; sc merge requires an interactive terminal to select the default branch")
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
