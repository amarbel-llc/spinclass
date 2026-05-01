package merge

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/log"

	tap "github.com/amarbel-llc/bob/packages/tap-dancer/go"
	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/worktree"
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

	return Resolved(execr, os.Stdout, nil, format, repoPath, wtPath, branch, defaultBranch, gitSync, inSession, verbose)
}

func Resolved(execr executor.Executor, w io.Writer, tw *tap.Writer, format, repoPath, wtPath, branch, defaultBranch string, gitSync bool, inSession bool, verbose bool) error {
	if info, err := os.Stat(repoPath); err != nil || !info.IsDir() {
		return fmt.Errorf("repository not found: %s", repoPath)
	}

	if defaultBranch == "" {
		var err error
		defaultBranch, err = ResolveDefaultBranch(repoPath)
		if err != nil {
			return err
		}
	}

	ownWriter := false
	if tw == nil && format == "tap" {
		tw = tap.NewWriter(w)
		ownWriter = true
	}

	if home, _ := os.UserHomeDir(); home != "" {
		hierarchy, hErr := sweatfile.LoadWorktreeHierarchy(home, repoPath, wtPath)
		if hErr == nil && hierarchy.Merged.DisableMergeEnabled() {
			source := disableMergeSource(hierarchy)
			msg := fmt.Sprintf(
				"merge disabled by sweatfile (disable-merge=true at %s); use `sc check` to run the pre-merge hook without merging",
				source,
			)
			if tw != nil {
				tw.NotOk("merge "+branch, map[string]string{
					"severity": "fail",
					"message":  msg,
				})
				if ownWriter {
					tw.Plan()
				}
			}
			return errors.New(msg)
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
			out, err := git.Pull(repoPath)
			if err != nil {
				diag := map[string]string{"severity": "fail", "message": err.Error()}
				if out != "" {
					diag["output"] = out
				}
				tw.NotOk("pull "+defaultBranch, diag)
				if ownWriter {
					tw.Plan()
				}
				return err
			}
			if verbose && out != "" {
				tw.OkDiag("pull "+defaultBranch, &tap.Diagnostics{Extras: map[string]any{"output": out}})
			} else {
				tw.Ok("pull " + defaultBranch)
			}
		} else {
			if err := git.RunPassthrough(repoPath, "pull"); err != nil {
				return err
			}
		}
	}

	if tw == nil {
		log.Info("rebasing onto "+defaultBranch, "worktree", branch)
	}

	if tw != nil {
		out, err := git.RunEnv(wtPath, []string{"GIT_SEQUENCE_EDITOR=true"}, "rebase", defaultBranch, "-i")
		if err != nil {
			diag := map[string]string{"severity": "fail", "message": err.Error()}
			if out != "" {
				diag["output"] = out
			}
			tw.NotOk("rebase "+branch, diag)
			if ownWriter {
				tw.Plan()
			}
			return err
		}
		if verbose && out != "" {
			tw.OkDiag("rebase "+branch, &tap.Diagnostics{Extras: map[string]any{"output": out}})
		} else {
			tw.Ok("rebase " + branch)
		}
	} else {
		if err := git.RunPassthroughEnv(wtPath, []string{"GIT_SEQUENCE_EDITOR=true"}, "rebase", defaultBranch, "-i"); err != nil {
			log.Error("rebase failed, not merging")
			return err
		}
	}

	if err := runPreMergeHook(tw, w, repoPath, wtPath, branch, ownWriter); err != nil {
		return err
	}

	if tw == nil {
		log.Info("merging worktree", "worktree", branch)
	}

	if tw != nil {
		out, err := git.Run(repoPath, "merge", "--ff-only", branch)
		if err != nil {
			diag := map[string]string{"severity": "fail", "message": err.Error()}
			if out != "" {
				diag["output"] = out
			}
			tw.NotOk("merge "+branch, diag)
			if ownWriter {
				tw.Plan()
			}
			return err
		}
		if verbose && out != "" {
			tw.OkDiag("merge "+branch, &tap.Diagnostics{Extras: map[string]any{"output": out}})
		} else {
			tw.Ok("merge " + branch)
		}
	} else {
		if err := git.RunPassthrough(repoPath, "merge", "--ff-only", branch); err != nil {
			log.Error("merge failed, not removing worktree")
			return err
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
			out, err := git.Run(repoPath, "worktree", "remove", wtPath)
			if err != nil {
				diag := map[string]string{"severity": "fail", "message": err.Error()}
				if out != "" {
					diag["output"] = out
				}
				tw.NotOk("remove worktree "+branch, diag)
				if ownWriter {
					tw.Plan()
				}
				return err
			}
			if verbose && out != "" {
				tw.OkDiag("remove worktree "+branch, &tap.Diagnostics{Extras: map[string]any{"output": out}})
			} else {
				tw.Ok("remove worktree " + branch)
			}
		} else {
			if err := git.RunPassthrough(repoPath, "worktree", "remove", wtPath); err != nil {
				return err
			}
		}

		if tw == nil {
			log.Info("deleting branch", "branch", branch)
		}

		if tw != nil {
			out, err := git.BranchDelete(repoPath, branch)
			if err != nil {
				diag := map[string]string{"severity": "fail", "message": err.Error()}
				if out != "" {
					diag["output"] = out
				}
				tw.NotOk("delete branch "+branch, diag)
				if ownWriter {
					tw.Plan()
				}
				return err
			}
			if verbose && out != "" {
				tw.OkDiag("delete branch "+branch, &tap.Diagnostics{Extras: map[string]any{"output": out}})
			} else {
				tw.Ok("delete branch " + branch)
			}
		} else {
			if _, err := git.BranchDelete(repoPath, branch); err != nil {
				return err
			}
		}
	}

	if gitSync {
		if tw == nil {
			log.Info("pushing", "repo", repoPath)
		}

		if tw != nil {
			out, err := git.Push(repoPath)
			if err != nil {
				diag := map[string]string{"severity": "fail", "message": err.Error()}
				if out != "" {
					diag["output"] = out
				}
				tw.NotOk("push", diag)
				if ownWriter {
					tw.Plan()
				}
				return err
			}
			if verbose && out != "" {
				tw.OkDiag("push", &tap.Diagnostics{Extras: map[string]any{"output": out}})
			} else {
				tw.Ok("push")
			}
		} else {
			if err := git.RunPassthrough(repoPath, "push"); err != nil {
				return err
			}
		}
	}

	if ownWriter {
		tw.Plan()
	}

	if inSession {
		// Remove session state file after successful merge
		session.Remove(repoPath, branch)
		return nil
	}

	// Outside session: request close if active, then clean up state
	executor.RequestClose(repoPath, branch)
	session.Remove(repoPath, branch)
	return nil
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

// runPreMergeHook loads the sweatfile hierarchy, runs the configured
// pre-merge hook (if any), and reports the outcome.
//
// When emitting TAP (tw != nil), the hook runs inside a tap-dancer
// OutputBlock so the `# Output:` header prints immediately and hook
// stdout/stderr streams as indented body lines in real time instead of
// being buffered until the hook exits.
func runPreMergeHook(tw *tap.Writer, w io.Writer, repoPath, wtPath, branch string, ownWriter bool) error {
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil
	}
	hierarchy, err := sweatfile.LoadWorktreeHierarchy(home, repoPath, wtPath)
	if err != nil {
		return nil
	}
	cmd := hierarchy.Merged.PreMergeHookCommand()
	if cmd == nil || *cmd == "" {
		return nil
	}
	desc := "pre-merge hook for " + branch + ": `" + *cmd + "`"

	if tw == nil {
		log.Info("running pre-merge hook", "worktree", branch)
		if err := hierarchy.Merged.RunPreMergeHook(wtPath, w); err != nil {
			log.Error("pre-merge hook failed, not merging")
			return err
		}
		return nil
	}

	var hookErr error
	tw.OutputBlock(desc, func(ob *tap.OutputBlockWriter) *tap.Diagnostics {
		lw := &lineWriter{ob: ob}
		hookErr = hierarchy.Merged.RunPreMergeHook(wtPath, lw)
		lw.Flush()
		if hookErr != nil {
			return &tap.Diagnostics{Severity: "fail", Message: hookErr.Error()}
		}
		return nil
	})
	if hookErr != nil && ownWriter {
		tw.Plan()
	}
	return hookErr
}

// lineWriter splits incoming bytes on '\n' and forwards each complete
// line to an OutputBlockWriter. Partial trailing content is buffered
// until a newline arrives or Flush() is called.
type lineWriter struct {
	ob  *tap.OutputBlockWriter
	buf []byte
}

func (lw *lineWriter) Write(p []byte) (int, error) {
	lw.buf = append(lw.buf, p...)
	for {
		i := bytes.IndexByte(lw.buf, '\n')
		if i < 0 {
			break
		}
		lw.ob.Line(string(lw.buf[:i]))
		lw.buf = lw.buf[i+1:]
	}
	return len(p), nil
}

func (lw *lineWriter) Flush() {
	if len(lw.buf) == 0 {
		return
	}
	lw.ob.Line(string(lw.buf))
	lw.buf = nil
}

// disableMergeSource returns the path of the most-specific sweatfile in
// the hierarchy that set DisableMerge to a non-nil value, or "<unknown>"
// if none can be located.
func disableMergeSource(h sweatfile.Hierarchy) string {
	for i := len(h.Sources) - 1; i >= 0; i-- {
		s := h.Sources[i]
		if !s.Found {
			continue
		}
		if s.File.Hooks != nil && s.File.Hooks.DisableMerge != nil {
			return s.Path
		}
	}
	return "<unknown>"
}
