package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrAmbiguousDefaultBranch = errors.New("both main and master branches exist")

func Run(repoPath string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimRight(string(exitErr.Stderr), "\n"))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func RunEnv(repoPath string, env []string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.Command("git", cmdArgs...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func RunPassthrough(repoPath string, args ...string) error {
	return RunPassthroughEnv(repoPath, nil, args...)
}

func RunPassthroughEnv(repoPath string, env []string, args ...string) error {
	cmdArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.Command("git", cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.Run()
}

func BranchCurrent(repoPath string) (string, error) {
	return Run(repoPath, "branch", "--show-current")
}

// RevParse resolves ref (e.g. "HEAD") to its full commit sha in repoPath.
func RevParse(repoPath, ref string) (string, error) {
	return Run(repoPath, "rev-parse", ref)
}

// RemoteURL returns the fetch URL configured for the "origin" remote of the
// repo (or worktree) at repoPath. It errors when no origin remote exists.
func RemoteURL(repoPath string) (string, error) {
	return Run(repoPath, "remote", "get-url", "origin")
}

func CommitsAhead(worktreePath, base, branch string) int {
	out, err := Run(worktreePath, "rev-list", base+".."+branch, "--count")
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(out)
	return n
}

func StatusPorcelain(path string) string {
	out, err := Run(path, "status", "--porcelain")
	if err != nil {
		return ""
	}
	return out
}

// UnmergedPaths returns the worktree's files with unresolved merge-conflict
// status (index stages 1/2/3), one path per entry, or nil when the worktree is
// conflict-free. A non-empty result means a rebase, merge, or stash pop left
// conflicts that must be resolved before any commit — git itself refuses to
// commit unmerged entries, so a caller about to auto-commit (e.g. a repair
// formatter) must check this first to avoid staging conflict markers.
func UnmergedPaths(path string) ([]string, error) {
	out, err := Run(path, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func RevListLeftRight(path string) (ahead, behind int) {
	out, err := Run(path, "rev-list", "--left-right", "--count", "@{upstream}...HEAD")
	if err != nil {
		return 0, 0
	}
	parts := strings.Fields(out)
	if len(parts) != 2 {
		return 0, 0
	}
	behind, _ = strconv.Atoi(parts[0])
	ahead, _ = strconv.Atoi(parts[1])
	return ahead, behind
}

func Upstream(path string) string {
	out, err := Run(path, "rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil {
		return ""
	}
	return out
}

func LastCommitDate(path string) string {
	out, err := Run(path, "log", "-1", "--format=%cs")
	if err != nil {
		return "n/a"
	}
	return out
}

func HasDirtyTracked(repoPath string) bool {
	cmd := exec.Command("git", "-C", repoPath, "diff", "--quiet")
	if err := cmd.Run(); err != nil {
		return true
	}
	cmd = exec.Command("git", "-C", repoPath, "diff", "--cached", "--quiet")
	if err := cmd.Run(); err != nil {
		return true
	}
	return false
}

func CheckoutFile(repoPath, file string) error {
	_, err := Run(repoPath, "checkout", "--", file)
	return err
}

func ResetFile(repoPath, file string) error {
	_, err := Run(repoPath, "reset", "HEAD", "--", file)
	return err
}

func WorktreeRemove(repoPath, worktreePath string) error {
	_, err := Run(repoPath, "worktree", "remove", worktreePath)
	return err
}

func WorktreeForceRemove(repoPath, worktreePath string) error {
	_, err := Run(repoPath, "worktree", "remove", "--force", worktreePath)
	return err
}

func BranchDelete(repoPath, branch string) (string, error) {
	return Run(repoPath, "branch", "-d", branch)
}

func BranchForceDelete(repoPath, branch string) (string, error) {
	return Run(repoPath, "branch", "-D", branch)
}

func BranchExists(repoPath, branch string) bool {
	_, err := Run(repoPath, "rev-parse", "--verify", "refs/heads/"+branch)
	return err == nil
}

func RemoteBranchExists(repoPath, branch string) bool {
	_, err := Run(repoPath, "rev-parse", "--verify", "refs/remotes/origin/"+branch)
	return err == nil
}

func DefaultBranch(repoPath string) (string, error) {
	hasMain := BranchExists(repoPath, "main")
	hasMaster := BranchExists(repoPath, "master")

	if hasMain && hasMaster {
		return "", ErrAmbiguousDefaultBranch
	}
	if hasMaster {
		return "master", nil
	}
	if hasMain {
		return "main", nil
	}

	out, err := Run(repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		branch := strings.TrimPrefix(out, "refs/remotes/origin/")
		if branch != "" {
			return branch, nil
		}
	}
	return BranchCurrent(repoPath)
}

func NewestFileTime(path string) time.Time {
	var newest time.Time
	_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Skip .git directories
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if !info.IsDir() && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest
}

func CommonDir(worktreePath string) (string, error) {
	out, err := Run(worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(worktreePath, out)
	}
	out = filepath.Clean(out)
	// Strip trailing .git to get the repo root
	if filepath.Base(out) == ".git" {
		out = filepath.Dir(out)
	}
	return out, nil
}

func Pull(repoPath string) (string, error) {
	return Run(repoPath, "pull")
}

func Push(repoPath string) (string, error) {
	return Run(repoPath, "push")
}

func Rebase(repoPath, onto string) (string, error) {
	return Run(repoPath, "rebase", onto)
}

// WorktreeAddFrom runs `git -C fromPath worktree add -b newBranch newPath`
// so the new worktree branches from fromPath's current HEAD.
func WorktreeAddFrom(fromPath, newBranch, newPath string) error {
	_, err := Run(fromPath, "worktree", "add", "-b", newBranch, newPath)
	return err
}

// WorktreePrune runs `git -C repoPath worktree prune`, clearing admin entries
// for worktrees whose directories no longer exist (e.g. crash-orphaned build
// worktrees). Best-effort; errors are returned for the caller to ignore.
func WorktreePrune(repoPath string) error {
	_, err := Run(repoPath, "worktree", "prune")
	return err
}

// WorktreeAddDetached runs `git -C fromPath worktree add --detach newPath ref`,
// checking ref out into a detached-HEAD worktree at newPath (no branch, so the
// same ref/branch can stay checked out elsewhere). Prunes stale admin entries
// first so a crash-orphaned path of the same name does not block the add.
func WorktreeAddDetached(fromPath, newPath, ref string) error {
	_ = WorktreePrune(fromPath)
	_, err := Run(fromPath, "worktree", "add", "--detach", newPath, ref)
	return err
}

// IsWorktree returns true if path contains a .git file (not directory),
// indicating it is a git worktree rather than the main repository.
func IsWorktree(path string) bool {
	info, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil {
		return false
	}
	return !info.IsDir()
}
