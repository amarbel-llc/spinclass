package merge

import (
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
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/worktree"
	tap "github.com/amarbel-llc/tap/go"
)

func Run(execr executor.Executor, format string, target string, gitSync bool, verbose bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	var repoPath, wtPath, branch string
	inSession := false

	if worktree.IsWorktree(cwd) && target == "" {
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
	} else {
		if worktree.IsWorktree(cwd) {
			repoPath, err = git.CommonDir(cwd)
		} else {
			repoPath, err = worktree.DetectRepo(cwd)
		}
		if err != nil {
			return fmt.Errorf("not in a git repository: %s", cwd)
		}

		if target != "" {
			wtPath, branch, err = ResolveWorktree(repoPath, target)
		} else {
			wtPath, branch, err = chooseWorktree(repoPath)
		}
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
// fully-resolved worktree. Returns any resource_link URIs emitted by the
// pre-merge hook (one per hook step that produced a madder blob; empty
// when madder is not pinned at build time) and a non-nil error if any
// step failed.
func Resolved(execr executor.Executor, w io.Writer, tw *tap.Writer, format, repoPath, wtPath, branch, defaultBranch string, gitSync bool, inSession bool, verbose bool) (blobURIs []string, err error) {
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
		tw = tap.NewWriter(w)
		ownWriter = true
		if embeds.MadderBin() != "" {
			tw.Comment("directive: if status is ok, the resource_link need not be followed; only inspect on failure")
		}
	}

	if home, _ := os.UserHomeDir(); home != "" {
		hierarchy, hErr := sweatfile.LoadWorktreeHierarchy(home, repoPath, wtPath)
		if hErr == nil && hierarchy.Merged.DisableMergeEnabled() {
			disableErr := fmt.Errorf(
				"merge disabled by sweatfile (disable-merge=true at %s); use `sc check` to run the pre-merge hook without merging",
				disableMergeSource(hierarchy),
			)
			return nil, failStep(tw, ownWriter, "merge "+branch, disableErr, "")
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
				return nil, failStep(tw, ownWriter, "pull "+defaultBranch, pullErr, out)
			}
			if verbose && out != "" {
				tw.OkDiag("pull "+defaultBranch, &tap.Diagnostics{Extras: map[string]any{"output": out}})
			} else {
				tw.Ok("pull " + defaultBranch)
			}
		} else {
			if pullErr := git.RunPassthrough(repoPath, "pull"); pullErr != nil {
				return nil, pullErr
			}
		}
	}

	if tw == nil {
		log.Info("rebasing onto "+defaultBranch, "worktree", branch)
	}

	if tw != nil {
		out, rebaseErr := git.RunEnv(wtPath, []string{"GIT_SEQUENCE_EDITOR=true"}, "rebase", defaultBranch, "-i")
		if rebaseErr != nil {
			return nil, failStep(tw, ownWriter, "rebase "+branch, rebaseErr, out)
		}
		if verbose && out != "" {
			tw.OkDiag("rebase "+branch, &tap.Diagnostics{Extras: map[string]any{"output": out}})
		} else {
			tw.Ok("rebase " + branch)
		}
	} else {
		if rebaseErr := git.RunPassthroughEnv(wtPath, []string{"GIT_SEQUENCE_EDITOR=true"}, "rebase", defaultBranch, "-i"); rebaseErr != nil {
			log.Error("rebase failed, not merging")
			return nil, rebaseErr
		}
	}

	// Short-circuit so an empty merge doesn't pay for the pre-merge hook.
	if git.CommitsAhead(wtPath, defaultBranch, branch) == 0 {
		noopErr := fmt.Errorf("nothing to merge: %s has no commits ahead of %s", branch, defaultBranch)
		if tw == nil {
			log.Error("nothing to merge", "branch", branch, "default", defaultBranch)
		}
		return nil, failStep(tw, ownWriter, "merge "+branch, noopErr, "")
	}

	hookURIs, hookErr := runPreMergeHook(tw, w, repoPath, wtPath, branch, ownWriter)
	blobURIs = append(blobURIs, hookURIs...)
	if hookErr != nil {
		return blobURIs, hookErr
	}

	if tw == nil {
		log.Info("merging worktree", "worktree", branch)
	}

	if tw != nil {
		out, mergeErr := git.Run(repoPath, "merge", "--ff-only", branch)
		if mergeErr != nil {
			return blobURIs, failStep(tw, ownWriter, "merge "+branch, mergeErr, out)
		}
		if verbose && out != "" {
			tw.OkDiag("merge "+branch, &tap.Diagnostics{Extras: map[string]any{"output": out}})
		} else {
			tw.Ok("merge " + branch)
		}
	} else {
		if mergeErr := git.RunPassthrough(repoPath, "merge", "--ff-only", branch); mergeErr != nil {
			log.Error("merge failed, not removing worktree")
			return blobURIs, mergeErr
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
				return blobURIs, failStep(tw, ownWriter, "remove worktree "+branch, removeErr, out)
			}
			if verbose && out != "" {
				tw.OkDiag("remove worktree "+branch, &tap.Diagnostics{Extras: map[string]any{"output": out}})
			} else {
				tw.Ok("remove worktree " + branch)
			}
		} else {
			if removeErr := git.RunPassthrough(repoPath, "worktree", "remove", wtPath); removeErr != nil {
				return blobURIs, removeErr
			}
		}

		if tw == nil {
			log.Info("deleting branch", "branch", branch)
		}

		if tw != nil {
			out, delErr := git.BranchDelete(repoPath, branch)
			if delErr != nil {
				return blobURIs, failStep(tw, ownWriter, "delete branch "+branch, delErr, out)
			}
			if verbose && out != "" {
				tw.OkDiag("delete branch "+branch, &tap.Diagnostics{Extras: map[string]any{"output": out}})
			} else {
				tw.Ok("delete branch " + branch)
			}
		} else {
			if _, delErr := git.BranchDelete(repoPath, branch); delErr != nil {
				return blobURIs, delErr
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
				return blobURIs, failStep(tw, ownWriter, "push", pushErr, out)
			}
			if verbose && out != "" {
				tw.OkDiag("push", &tap.Diagnostics{Extras: map[string]any{"output": out}})
			} else {
				tw.Ok("push")
			}
		} else {
			if pushErr := git.RunPassthrough(repoPath, "push"); pushErr != nil {
				return blobURIs, pushErr
			}
		}
	}

	if ownWriter {
		tw.Plan()
	}

	if inSession {
		// Session state stays put. spinclass worktrees are workers — they
		// host many sequences of work separated by `merge-this-session`,
		// and tearing down state.json + the central index symlink here
		// would orphan the worktree from `sc list`/`resume`/`close` until
		// the next session.Write. Cleanup is owned by `sc close`/`sc clean`.
		return blobURIs, nil
	}

	// Outside session: request graceful close if the target is still
	// running. State cleanup is delegated to the close path
	// (closeShop → close.RunResolved → session.Tombstone) when conditions
	// warrant; abandoned state is reaped by `sc clean`.
	executor.RequestClose(repoPath, branch)
	return blobURIs, nil
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
// resolvable or the hierarchy fails to load. Returned blobURIs are the
// resource_link URIs emitted for hook output (compact path only). In
// passthrough mode, emits the legacy "running pre-merge hook" /
// "pre-merge hook failed, not merging" log lines that operators rely
// on.
func runPreMergeHook(tw *tap.Writer, w io.Writer, repoPath, wtPath, branch string, ownWriter bool) ([]string, error) {
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil, nil
	}
	hierarchy, err := sweatfile.LoadWorktreeHierarchy(home, repoPath, wtPath)
	if err != nil {
		return nil, nil
	}
	if tw == nil {
		cmd := hierarchy.Merged.PreMergeHookCommand()
		if cmd == nil || *cmd == "" {
			return nil, nil
		}
		log.Info("running pre-merge hook", "worktree", branch)
		if _, err := check.RunWithWriter(nil, w, hierarchy, wtPath, branch, ownWriter); err != nil {
			log.Error("pre-merge hook failed, not merging")
			return nil, err
		}
		return nil, nil
	}
	return check.RunWithWriter(tw, w, hierarchy, wtPath, branch, ownWriter)
}

// failStep emits a TAP NotOk for label populated from err
// (severity=fail), optionally including a verbose output field. When
// ownWriter is true, follows with tw.Plan() to terminate the stream.
// tw=nil skips emit. Returns err unchanged so callers can write
// `return failStep(...)`.
func failStep(tw *tap.Writer, ownWriter bool, label string, err error, output string) error {
	if tw == nil {
		return err
	}
	diag := map[string]string{"severity": "fail", "message": err.Error()}
	if output != "" {
		diag["output"] = output
	}
	tw.NotOk(label, diag)
	if ownWriter {
		tw.Plan()
	}
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
