package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/git"
)

const WorktreesDir = ".worktrees"

type ResolvedPath struct {
	AbsPath        string // absolute filesystem path to the worktree
	RepoPath       string // absolute path to the parent git repo
	SessionKey     string // key for clown/executor sessions (<repo-dirname>/<branch>)
	Branch         string // branch name
	Description    string // freeform session description
	ExistingBranch string // non-empty when an existing branch was detected
}

// ResolvePath resolves a worktree target relative to a git repo.
//
// A random branch name is always generated. Args, if provided, are joined
// as a freeform session description (not used as the branch name).
//
// SessionKey is always <repo-dirname>/<branch>.
func ResolvePath(
	repoPath string,
	args []string,
) (ResolvedPath, error) {
	branch := RandomName(repoPath)
	absPath := filepath.Join(repoPath, WorktreesDir, branch)
	repoDirname := filepath.Base(repoPath)

	description := strings.Join(args, " ")

	return ResolvedPath{
		AbsPath:     absPath,
		RepoPath:    repoPath,
		SessionKey:  repoDirname + "/" + branch,
		Branch:      branch,
		Description: description,
	}, nil
}

// DetectRepo walks up from dir to the nearest main-checkout root (a .git
// DIRECTORY, not a worktree's .git file), honouring GIT_CEILING_DIRECTORIES.
// The implementation is git.DetectRepo; this wrapper keeps the worktree-facing
// name for callers that already import this package.
func DetectRepo(dir string) (string, error) {
	return git.DetectRepo(dir)
}

// IsWorktree returns true if path contains a .git file (not directory),
// indicating it is a git worktree rather than the main repository.
func IsWorktree(path string) bool {
	return git.IsWorktree(path)
}

// FillBranchFromGit populates the Branch field from git.
func (rp *ResolvedPath) FillBranchFromGit() error {
	branch, err := git.BranchCurrent(rp.AbsPath)
	if err != nil {
		return err
	}
	rp.Branch = branch
	return nil
}

// ScanRepos scans for repositories that have a WorktreesDir directory.
// If startDir itself is a repo with WorktreesDir, returns just that path.
// Otherwise scans immediate children for repos with WorktreesDir.
func ScanRepos(startDir string) []string {
	if isRepoWithWorktrees(startDir) {
		return []string{startDir}
	}

	entries, err := os.ReadDir(startDir)
	if err != nil {
		return nil
	}

	var repos []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(startDir, entry.Name())
		if isRepoWithWorktrees(child) {
			repos = append(repos, child)
		}
	}
	return repos
}

func isRepoWithWorktrees(dir string) bool {
	gitInfo, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil || !gitInfo.IsDir() {
		return false
	}
	wtInfo, err := os.Stat(filepath.Join(dir, WorktreesDir))
	if err != nil || !wtInfo.IsDir() {
		return false
	}
	return true
}

// ListWorktrees returns absolute paths of all worktree directories in
// <repoPath>/<WorktreesDir>/.
func ListWorktrees(repoPath string) []string {
	wtDir := filepath.Join(repoPath, WorktreesDir)
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		return nil
	}

	var worktrees []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		wtPath := filepath.Join(wtDir, entry.Name())
		if IsWorktree(wtPath) {
			worktrees = append(worktrees, wtPath)
		}
	}
	return worktrees
}

// ForkName returns a collision-free branch name for forking sourceBranch.
// It tries <sourceBranch>-1, <sourceBranch>-2, etc., skipping any suffix that
// collides with an existing directory in <repoPath>/.worktrees/ OR an existing
// local/remote branch. The branch check mirrors RandomName's (#207): a
// lingering <sourceBranch>-N branch whose worktree was removed must not be
// re-adopted by `git worktree add -b`, which would hard-fail on it.
func ForkName(repoPath, sourceBranch string) string {
	wtDir := filepath.Join(repoPath, WorktreesDir)
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s-%d", sourceBranch, n)
		if _, err := os.Stat(filepath.Join(wtDir, candidate)); !os.IsNotExist(err) {
			continue
		}
		if git.BranchExists(repoPath, candidate) || git.RemoteBranchExists(repoPath, candidate) {
			continue
		}
		return candidate
	}
}
