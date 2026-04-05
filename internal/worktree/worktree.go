package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amarbel-llc/spinclass/internal/claude"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/prompt"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

const WorktreesDir = ".worktrees"

type ResolvedPath struct {
	AbsPath        string            // absolute filesystem path to the worktree
	RepoPath       string            // absolute path to the parent git repo
	SessionKey     string            // key for zmx/executor sessions (<repo-dirname>/<branch>)
	Branch         string            // branch name
	Description    string            // freeform session description
	ExistingBranch string            // non-empty when an existing branch was detected
	Issue          *prompt.IssueData // optional issue context for session prompt
	PR             *prompt.PRData    // optional PR context for session prompt
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

func detectBranch(repoPath string, candidates ...string) (string, string) {
	seen := make(map[string]bool)
	var unique []string
	for _, c := range candidates {
		if c != "" && !seen[c] {
			seen[c] = true
			unique = append(unique, c)
		}
	}

	for _, name := range unique {
		if git.BranchExists(repoPath, name) {
			return name, name
		}
	}
	for _, name := range unique {
		if git.RemoteBranchExists(repoPath, name) {
			return name, name
		}
	}

	// No existing branch found — use the last candidate (most transformed).
	return unique[len(unique)-1], ""
}

// DetectRepo walks up from dir looking for a .git directory (must be a
// directory, not a file — files indicate worktrees). Respects
// GIT_CEILING_DIRECTORIES to prevent discovery above certain paths.
// Returns the repo root.
func DetectRepo(dir string) (string, error) {
	dir = filepath.Clean(dir)
	ceilings := parseCeilingDirs()

	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Lstat(gitPath)
		if err == nil && info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir || isCeiling(dir, ceilings) {
			return "", fmt.Errorf("no git repository found from %s", dir)
		}
		dir = parent
	}
}

func parseCeilingDirs() []string {
	env := os.Getenv("GIT_CEILING_DIRECTORIES")
	if env == "" {
		return nil
	}

	var dirs []string
	for _, d := range filepath.SplitList(env) {
		if clean := filepath.Clean(d); filepath.IsAbs(clean) {
			dirs = append(dirs, clean)
		}
	}
	return dirs
}

func isCeiling(dir string, ceilings []string) bool {
	for _, c := range ceilings {
		if dir == c {
			return true
		}
	}
	return false
}

// Create creates a new git worktree and applies sweatfile configuration.
// If existingBranch is non-empty, the worktree checks out that branch
// instead of creating a new one from the directory name.
func Create(
	repoPath, worktreePath, existingBranch string,
	issue *prompt.IssueData, pr *prompt.PRData,
) (sweatfile.Hierarchy, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return sweatfile.Hierarchy{}, fmt.Errorf(
			"getting home directory: %w",
			err,
		)
	}

	sweetfile, err := sweatfile.LoadHierarchy(home, repoPath)
	if err != nil {
		return sweetfile, fmt.Errorf("loading sweatfile: %w", err)
	}

	if existingBranch != "" {
		if err := git.RunPassthrough(repoPath, "worktree", "add", worktreePath, existingBranch); err != nil {
			return sweatfile.Hierarchy{}, fmt.Errorf(
				"git worktree add: %w",
				err,
			)
		}
	} else {
		if err := git.RunPassthrough(repoPath, "worktree", "add", worktreePath); err != nil {
			return sweatfile.Hierarchy{}, fmt.Errorf("git worktree add: %w", err)
		}
	}

	return sweetfile, applyWorktreeConfig(
		home,
		sweetfile,
		repoPath,
		worktreePath,
		issue,
		pr,
	)
}

// CreateFrom creates a new worktree branched from fromPath's current HEAD.
// It runs git worktree add -b from fromPath, then applies sweatfile and
// trusts the workspace, same as Create.
func CreateFrom(
	repoPath, fromPath, newPath, newBranch string,
) (sweatfile.Hierarchy, error) {
	if err := git.WorktreeAddFrom(fromPath, newBranch, newPath); err != nil {
		return sweatfile.Hierarchy{}, fmt.Errorf("git worktree add: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return sweatfile.Hierarchy{}, fmt.Errorf(
			"getting home directory: %w",
			err,
		)
	}

	sweetfile, err := sweatfile.LoadHierarchy(home, repoPath)
	if err != nil {
		return sweetfile, fmt.Errorf("loading sweatfile: %w", err)
	}

	return sweetfile, applyWorktreeConfig(home, sweetfile, repoPath, newPath, nil, nil)
}

// applyWorktreeConfig excludes .worktrees from git, loads and applies
// sweatfile,
// and trusts worktreePath in Claude.
func applyWorktreeConfig(
	home string,
	sweetfile sweatfile.Hierarchy,
	repoPath string,
	worktreePath string,
	issue *prompt.IssueData,
	pr *prompt.PRData,
) error {
	if err := applyGitExcludes(repoPath, sweetfile.Merged.GitExcludes()); err != nil {
		return fmt.Errorf("applying git excludes: %w", err)
	}

	tmpDir := filepath.Join(worktreePath, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("creating .tmp directory: %w", err)
	}

	if err := sweetfile.Merged.Apply(worktreePath); err != nil {
		return fmt.Errorf("applying sweatfile: %w", err)
	}

	remoteURL := ""
	if out, err := git.Run(repoPath, "remote", "get-url", "origin"); err == nil {
		remoteURL = strings.TrimSpace(out)
	}

	isFork, ownerType, ownerLogin := prompt.FetchRepoMetadata(repoPath)
	branch := filepath.Base(worktreePath)
	repoDirname := filepath.Base(repoPath)

	writeOpts := prompt.WriteOptions{
		WorktreePath: worktreePath,
		RepoPath:     repoPath,
		RemoteURL:    remoteURL,
		Branch:       branch,
		SessionID:    repoDirname + "/" + branch,
		IsFork:       isFork,
		OwnerType:    ownerType,
		OwnerLogin:   ownerLogin,
		Issue:        issue,
		PR:           pr,
	}

	if err := prompt.WriteSessionContext(writeOpts); err != nil {
		return fmt.Errorf("writing session context: %w", err)
	}

	claudeJSONPath := filepath.Join(home, ".claude.json")
	if err := claude.TrustWorkspace(claudeJSONPath, worktreePath); err != nil {
		return fmt.Errorf("trusting workspace in claude: %w", err)
	}

	if err := claude.WriteMCPConfig(worktreePath); err != nil {
		return fmt.Errorf("writing .mcp.json: %w", err)
	}

	if err := sweetfile.Merged.RunCreateHook(worktreePath); err != nil {
		git.RunPassthrough(
			repoPath,
			"worktree",
			"remove",
			"--force",
			worktreePath,
		)
		return fmt.Errorf("create hook failed: %w", err)
	}

	return nil
}

const (
	excludeMarkerStart = "# --- spinclass-managed ---"
	excludeMarkerEnd   = "# --- spinclass-managed-end ---"
)

// applyGitExcludes writes all excludes into a fenced block in
// .git/info/exclude. The block is replaced on each call, making it
// idempotent. Lines outside the fenced block are preserved.
func applyGitExcludes(repoPath string, excludes []string) error {
	excludePath := filepath.Join(repoPath, ".git", "info", "exclude")

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return err
	}

	var preserved []string
	if data, err := os.ReadFile(excludePath); err == nil {
		lines := strings.Split(string(data), "\n")
		inBlock := false
		for _, line := range lines {
			switch {
			case line == excludeMarkerStart:
				inBlock = true
			case line == excludeMarkerEnd:
				inBlock = false
			case !inBlock:
				preserved = append(preserved, line)
			}
		}
		// strings.Split produces an empty final element from a trailing
		// newline — drop it so we don't accumulate blank lines.
		if len(preserved) > 0 && preserved[len(preserved)-1] == "" {
			preserved = preserved[:len(preserved)-1]
		}
	}

	var buf strings.Builder
	for _, line := range preserved {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	buf.WriteString(excludeMarkerStart)
	buf.WriteByte('\n')
	for _, exc := range excludes {
		buf.WriteString(exc)
		buf.WriteByte('\n')
	}
	buf.WriteString(excludeMarkerEnd)
	buf.WriteByte('\n')

	return os.WriteFile(excludePath, []byte(buf.String()), 0o644)
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
// It tries <sourceBranch>-1, <sourceBranch>-2, etc., checking for existing
// directories in <repoPath>/.worktrees/.
func ForkName(repoPath, sourceBranch string) string {
	wtDir := filepath.Join(repoPath, WorktreesDir)
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s-%d", sourceBranch, n)
		_, err := os.Stat(filepath.Join(wtDir, candidate))
		if os.IsNotExist(err) {
			return candidate
		}
	}
}
