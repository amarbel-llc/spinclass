package merge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/log"

	"github.com/amarbel-llc/spinclass/internal/check"
	"github.com/amarbel-llc/spinclass/internal/embeds"
	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/sweatfileio"
	"github.com/amarbel-llc/spinclass/internal/worktree"
	tap "github.com/amarbel-llc/tap/go/pkgs/writer"
	"github.com/amarbel-llc/tap/go/pkgs/yaml_diagnostic"
)

func Run(execr executor.Executor, format string, target string, gitSync bool, verbose bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
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

	defaultBranch, err := ResolveDefaultBranch(repoPath)
	if err != nil {
		return err
	}

	_, err = Resolved(execr, os.Stdout, nil, format, repoPath, wtPath, branch, defaultBranch, gitSync, inSession, verbose)
	return err
}

// Resolved orchestrates the rebase/pre-merge-hook/merge sequence for a
// fully-resolved worktree. Returns any resource_link blobs emitted by
// the pre-merge hook (one per hook step that produced a madder blob;
// empty when madder is not pinned at build time) and a non-nil error if
// any step failed. Each BlobLink carries the MIME type matching the
// format the blob was written in.
func Resolved(execr executor.Executor, w io.Writer, tw *tap.Writer, format, repoPath, wtPath, branch, defaultBranch string, gitSync bool, inSession bool, verbose bool) (blobLinks []check.BlobLink, err error) {
	return ResolvedContext(context.Background(), execr, w, tw, format, repoPath, wtPath, branch, defaultBranch, gitSync, inSession, verbose, nil)
}

// ResolvedContext is Resolved bound to ctx with an optional activity writer.
// ctx threads to the pre-merge hook subprocess (cancellable by the async job
// runner); activity, when non-nil, is teed the hook's live output (the async
// job log). Synchronous callers use Resolved (background ctx, nil activity).
func ResolvedContext(ctx context.Context, execr executor.Executor, w io.Writer, tw *tap.Writer, format, repoPath, wtPath, branch, defaultBranch string, gitSync bool, inSession bool, verbose bool, activity io.Writer) (blobLinks []check.BlobLink, err error) {
	if info, statErr := os.Stat(repoPath); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("repository not found: %s", repoPath)
	}

	if defaultBranch == "" {
		defaultBranch, err = ResolveDefaultBranch(repoPath)
		if err != nil {
			return nil, err
		}
	}

	ownWriter := false
	if tw == nil && format == "tap" {
		tw = NewMergeWriter(w)
		ownWriter = true
	}

	pinnedSha, prepErr := PrepareMerge(tw, w, repoPath, wtPath, branch, defaultBranch, gitSync, verbose)
	if prepErr != nil {
		if ownWriter {
			tw.Plan()
		}
		return nil, prepErr
	}

	blobLinks, err = FinishMerge(ctx, execr, tw, w, repoPath, wtPath, branch, defaultBranch, pinnedSha, gitSync, inSession, verbose, activity)
	if ownWriter {
		tw.Plan()
	}
	return blobLinks, err
}

// NewMergeWriter creates the tap.Writer used for merge/check TAP output,
// emitting the madder resource_link directive comment when madder is pinned at
// build time. The caller owns Plan(). Exported so the async merge tool can share
// one writer/buffer across the synchronous PrepareMerge prefix and the
// backgrounded FinishMerge suffix.
func NewMergeWriter(w io.Writer) *tap.Writer {
	tw := tap.NewWriter(w)
	if embeds.MadderBin() != "" {
		tw.Comment("directive: if status is ok, the resource_link need not be followed; only inspect on failure")
	}
	return tw
}

// PrepareMerge runs the fast, session-worktree-touching prefix of a merge: the
// disable-merge gate, optional pull of defaultBranch, rebase of branch onto it,
// and the nothing-to-merge short-circuit. On success it returns the pinned
// post-rebase HEAD sha — the exact commit FinishMerge verifies and merges, so a
// commit landing on branch after PrepareMerge returns does not change what gets
// merged. tw may be nil (passthrough). Never calls tw.Plan(); the caller owns
// stream termination.
//
// Splitting prepare from finish lets the async merge tool run this prefix
// synchronously (before returning the job id), freeing the session worktree the
// moment the rebase lands while FinishMerge's slow pre-merge hook runs detached
// in an isolated build worktree.
func PrepareMerge(tw *tap.Writer, w io.Writer, repoPath, wtPath, branch, defaultBranch string, gitSync, verbose bool) (pinnedSha string, err error) {
	if home, _ := os.UserHomeDir(); home != "" {
		hierarchy, hErr := sweatfileio.LoadWorktreeHierarchy(home, repoPath, wtPath)
		if hErr == nil && hierarchy.Merged.DisableMergeEnabled() {
			disableErr := fmt.Errorf(
				"merge disabled by sweatfile (disable-merge=true at %s); use `sc check` to run the pre-merge hook without merging",
				disableMergeSource(hierarchy),
			)
			return "", failStep(tw, "merge "+branch, disableErr, "")
		}
	}

	// Pull the default branch BEFORE rebasing, so the session branch is
	// rebased onto the current origin tip rather than a stale local ref.
	// Otherwise a concurrent commit on origin/<default> arriving between
	// session start and merge leaves `git merge --ff-only` unable to
	// fast-forward. See #29.
	if gitSync {
		if tw == nil {
			log.Info("pulling "+defaultBranch, "repo", repoPath)
		}

		if tw != nil {
			out, pullErr := git.Pull(repoPath)
			if pullErr != nil {
				return "", failStep(tw, "pull "+defaultBranch, pullErr, out)
			}
			if verbose && out != "" {
				tw.OkDiag("pull "+defaultBranch, &yaml_diagnostic.YAMLDiagnostic{Extras: map[string]any{"output": out}})
			} else {
				tw.Ok("pull " + defaultBranch)
			}
		} else {
			if pullErr := git.RunPassthrough(repoPath, "pull"); pullErr != nil {
				return "", pullErr
			}
		}
	}

	if tw == nil {
		log.Info("rebasing onto "+defaultBranch, "worktree", branch)
	}

	if tw != nil {
		out, rebaseErr := git.RunEnv(wtPath, []string{"GIT_SEQUENCE_EDITOR=true"}, "rebase", defaultBranch, "-i")
		if rebaseErr != nil {
			return "", failStep(tw, "rebase "+branch, rebaseErr, out)
		}
		if verbose && out != "" {
			tw.OkDiag("rebase "+branch, &yaml_diagnostic.YAMLDiagnostic{Extras: map[string]any{"output": out}})
		} else {
			tw.Ok("rebase " + branch)
		}
	} else {
		if rebaseErr := git.RunPassthroughEnv(wtPath, []string{"GIT_SEQUENCE_EDITOR=true"}, "rebase", defaultBranch, "-i"); rebaseErr != nil {
			log.Error("rebase failed, not merging")
			return "", rebaseErr
		}
	}

	// Short-circuit so an empty merge doesn't pay for the pre-merge hook.
	if git.CommitsAhead(wtPath, defaultBranch, branch) == 0 {
		noopErr := fmt.Errorf("nothing to merge: %s has no commits ahead of %s", branch, defaultBranch)
		if tw == nil {
			log.Error("nothing to merge", "branch", branch, "default", defaultBranch)
		}
		return "", failStep(tw, "merge "+branch, noopErr, "")
	}

	// Pin the post-rebase tip: FinishMerge verifies and merges exactly this sha,
	// so work committed onto branch while the hook runs is left for a later merge.
	pinnedSha, shaErr := git.RevParse(wtPath, "HEAD")
	if shaErr != nil {
		return "", failStep(tw, "merge "+branch, fmt.Errorf("could not resolve %s HEAD: %w", branch, shaErr), "")
	}
	return pinnedSha, nil
}

// FinishMerge runs the slow, committing suffix of a merge against pinnedSha (the
// sha PrepareMerge returned): the pre-merge hook (run in an isolated detached
// build worktree pinned to pinnedSha unless [hooks].disable-merge-build-worktree
// is set), the ff-only merge of pinnedSha into defaultBranch, optional
// worktree/branch teardown, and push. Never calls tw.Plan(); the caller owns
// stream termination.
func FinishMerge(ctx context.Context, execr executor.Executor, tw *tap.Writer, w io.Writer, repoPath, wtPath, branch, defaultBranch, pinnedSha string, gitSync, inSession, verbose bool, activity io.Writer) (blobLinks []check.BlobLink, err error) {
	hookLinks, hookErr := runPreMergeHookContext(ctx, tw, w, repoPath, wtPath, branch, pinnedSha, activity)
	blobLinks = append(blobLinks, hookLinks...)
	if hookErr != nil {
		return blobLinks, hookErr
	}

	if tw == nil {
		log.Info("merging worktree", "worktree", branch)
	}

	if tw != nil {
		out, mergeErr := git.Run(repoPath, "merge", "--ff-only", pinnedSha)
		if mergeErr != nil {
			return blobLinks, failStep(tw, "merge "+branch, mergeErr, out)
		}
		if verbose && out != "" {
			tw.OkDiag("merge "+branch, &yaml_diagnostic.YAMLDiagnostic{Extras: map[string]any{"output": out}})
		} else {
			tw.Ok("merge " + branch)
		}
	} else {
		if mergeErr := git.RunPassthrough(repoPath, "merge", "--ff-only", pinnedSha); mergeErr != nil {
			log.Error("merge failed, not removing worktree")
			return blobLinks, mergeErr
		}
	}

	// Skip worktree removal when running from inside the worktree being
	// merged (can't remove cwd) or when inside an active session.
	insideWorktree := false
	if cwd, err := os.Getwd(); err == nil {
		insideWorktree = isInsideWorktree(cwd, wtPath)
	}

	if !inSession && !insideWorktree {
		if tw == nil {
			log.Info("removing worktree", "path", wtPath)
		}

		if tw != nil {
			out, removeErr := git.Run(repoPath, "worktree", "remove", wtPath)
			if removeErr != nil {
				return blobLinks, failStep(tw, "remove worktree "+branch, removeErr, out)
			}
			if verbose && out != "" {
				tw.OkDiag("remove worktree "+branch, &yaml_diagnostic.YAMLDiagnostic{Extras: map[string]any{"output": out}})
			} else {
				tw.Ok("remove worktree " + branch)
			}
		} else {
			if removeErr := git.RunPassthrough(repoPath, "worktree", "remove", wtPath); removeErr != nil {
				return blobLinks, removeErr
			}
		}

		if tw == nil {
			log.Info("deleting branch", "branch", branch)
		}

		if tw != nil {
			out, delErr := git.BranchDelete(repoPath, branch)
			if delErr != nil {
				return blobLinks, failStep(tw, "delete branch "+branch, delErr, out)
			}
			if verbose && out != "" {
				tw.OkDiag("delete branch "+branch, &yaml_diagnostic.YAMLDiagnostic{Extras: map[string]any{"output": out}})
			} else {
				tw.Ok("delete branch " + branch)
			}
		} else {
			if _, delErr := git.BranchDelete(repoPath, branch); delErr != nil {
				return blobLinks, delErr
			}
		}
	}

	if gitSync {
		if tw == nil {
			log.Info("pushing", "repo", repoPath)
		}

		if tw != nil {
			out, pushErr := git.Push(repoPath)
			if pushErr != nil {
				return blobLinks, failStep(tw, "push", pushErr, out)
			}
			if verbose && out != "" {
				tw.OkDiag("push", &yaml_diagnostic.YAMLDiagnostic{Extras: map[string]any{"output": out}})
			} else {
				tw.Ok("push")
			}
		} else {
			if pushErr := git.RunPassthrough(repoPath, "push"); pushErr != nil {
				return blobLinks, pushErr
			}
		}
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
	executor.RequestClose(repoPath, branch)
	return blobLinks, nil
}

// MergeImplicit runs the merge path for a main-checkout (implicit) session:
// the pre-merge hook against HEAD, then a push of the current (default) branch.
// There is no rebase or ff-merge — the work is already on the default branch.
// The push is surfaced as its own TAP step so it is never silent. Mirrors
// FinishMerge's tw/failStep/verbose idioms; the caller owns the writer and
// Plan(). hookSha pins the exact committed sha the hook verifies.
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
// CAVEAT: runPreMergeHookContext runs the hook in an isolated build worktree
// at filepath.Join(filepath.Dir(wtPath), name). For a normal worktree session
// wtPath is <repo>/.worktrees/<branch>, so the build worktree lands inside
// .worktrees/. But for an implicit session wtPath == checkout == the repo
// root, so filepath.Dir(checkout) is the repo's PARENT dir — the build
// worktree is created as a sibling of the repo root, OUTSIDE the repo (e.g.
// for a repo at ~/eng/repos/myrepo the build worktree lands at
// ~/eng/repos/.merge-master-<sha>-<pid>). Known placement quirk, tracked as a
// followup (see #NNN).
func MergeImplicit(ctx context.Context, tw *tap.Writer, w io.Writer, repoPath, checkout, branch string, verbose bool, activity io.Writer) (blobLinks []check.BlobLink, err error) {
	if home, _ := os.UserHomeDir(); home != "" {
		hierarchy, hErr := sweatfileio.LoadWorktreeHierarchy(home, repoPath, checkout)
		if hErr == nil && hierarchy.Merged.DisableMergeEnabled() {
			disableErr := fmt.Errorf(
				"merge disabled by sweatfile (disable-merge=true at %s); use `sc check` to run the pre-merge hook without merging",
				disableMergeSource(hierarchy),
			)
			return nil, failStep(tw, "merge "+branch, disableErr, "")
		}
	}

	// Pin HEAD; the hook verifies exactly this committed sha.
	pinnedSha, shaErr := git.RevParse(checkout, "HEAD")
	if shaErr != nil {
		return nil, failStep(tw, "merge "+branch, fmt.Errorf("could not resolve HEAD: %w", shaErr), "")
	}

	// Pre-merge hook (isolated build worktree pinned to pinnedSha).
	hookLinks, hookErr := runPreMergeHookContext(ctx, tw, w, repoPath, checkout, branch, pinnedSha, activity)
	blobLinks = append(blobLinks, hookLinks...)
	if hookErr != nil {
		return blobLinks, hookErr
	}

	// Push the default branch — outward-facing, so it's a distinct TAP step.
	if tw == nil {
		log.Info("pushing", "branch", branch)
	}

	if tw != nil {
		out, pushErr := git.Push(checkout)
		if pushErr != nil {
			return blobLinks, failStep(tw, "push "+branch, pushErr, out)
		}
		if verbose && out != "" {
			tw.OkDiag("push "+branch, &yaml_diagnostic.YAMLDiagnostic{Extras: map[string]any{"output": out}})
		} else {
			tw.Ok("push " + branch)
		}
	} else {
		if pushErr := git.RunPassthrough(checkout, "push"); pushErr != nil {
			return blobLinks, pushErr
		}
	}
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

// runPreMergeHook loads the sweatfile hierarchy and runs the configured
// pre-merge hook. Returns (nil, nil) silently when home is not
// resolvable or the hierarchy fails to load. Returned BlobLinks are the
// resource_link blobs emitted for hook output (compact path only). In
// passthrough mode, emits the legacy "running pre-merge hook" /
// "pre-merge hook failed, not merging" log lines that operators rely
// on.
func runPreMergeHookContext(ctx context.Context, tw *tap.Writer, w io.Writer, repoPath, wtPath, branch, hookSha string, activity io.Writer) ([]check.BlobLink, error) {
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil, nil
	}
	hierarchy, err := sweatfileio.LoadWorktreeHierarchy(home, repoPath, wtPath)
	if err != nil {
		return nil, nil
	}
	// ownWriter=false: the merge orchestrator (ResolvedContext / the async
	// handler) owns Plan(); check only emits the hook test point here.
	if tw == nil {
		cmd := hierarchy.Merged.PreMergeHookCommand()
		if cmd == nil || *cmd == "" {
			return nil, nil
		}
		log.Info("running pre-merge hook", "worktree", branch)
		if _, err := check.RunWithWriterContext(ctx, nil, w, hierarchy, wtPath, branch, hookSha, false, activity); err != nil {
			log.Error("pre-merge hook failed, not merging")
			return nil, err
		}
		return nil, nil
	}
	return check.RunWithWriterContext(ctx, tw, w, hierarchy, wtPath, branch, hookSha, false, activity)
}

// failStep emits a TAP NotOk for label populated from err (severity=fail),
// optionally including a verbose output field. tw=nil skips emit. Never calls
// tw.Plan() — the merge orchestrator (ResolvedContext / the async handler) owns
// stream termination so exactly one plan line is emitted per run. Returns err
// unchanged so callers can write `return failStep(...)`.
func failStep(tw *tap.Writer, label string, err error, output string) error {
	if tw == nil {
		return err
	}
	diag := map[string]string{"severity": "fail", "message": err.Error()}
	if output != "" {
		diag["output"] = output
	}
	tw.NotOk(label, diag)
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
