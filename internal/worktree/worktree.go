package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"code.linenisgreat.com/spinclass/internal/claude"
	"code.linenisgreat.com/spinclass/internal/dodder"
	"code.linenisgreat.com/spinclass/internal/embeds"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/madder"
	"code.linenisgreat.com/spinclass/internal/setupfingerprint"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
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
) (sweatfile.Hierarchy, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return sweatfile.Hierarchy{}, fmt.Errorf(
			"getting home directory: %w",
			err,
		)
	}

	sweetfile, err := sweatfileio.LoadHierarchy(home, repoPath)
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
		// Pass -b explicitly so a name matching a pre-existing branch is a
		// hard error rather than a silent checkout of that branch. Adopting an
		// existing branch is reserved for the existingBranch != "" path above;
		// under the fresh-session path it would replay stale (possibly
		// unmerged) history in a supposedly new worktree (#207).
		branch := filepath.Base(worktreePath)
		if err := git.RunPassthrough(repoPath, "worktree", "add", "-b", branch, worktreePath); err != nil {
			return sweatfile.Hierarchy{}, fmt.Errorf("git worktree add: %w", err)
		}
	}

	return sweetfile, applyWorktreeConfig(
		home,
		sweetfile,
		repoPath,
		worktreePath,
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

	sweetfile, err := sweatfileio.LoadHierarchy(home, repoPath)
	if err != nil {
		return sweetfile, fmt.Errorf("loading sweatfile: %w", err)
	}

	return sweetfile, applyWorktreeConfig(home, sweetfile, repoPath, newPath)
}

// Reapply re-runs the worktree setup (applyWorktreeConfig) on an EXISTING
// worktree, WITHOUT a `git worktree add`. It is the seam `sc rebuild` and
// resume auto-rebuild use to refresh a worktree whose installed setup has
// drifted from current config/binary/pins. It loads the same repo-level
// hierarchy `Create` does (LoadHierarchy — NOT the worktree-leaf-inclusive
// LoadWorktreeHierarchy), so a rebuild reproduces exactly what `sc start`
// applied and the setup fingerprint stays comparable across the two. Returns
// the merged hierarchy so the caller can compute and record the fresh
// fingerprint.
func Reapply(repoPath, worktreePath string) (sweatfile.Hierarchy, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return sweatfile.Hierarchy{}, fmt.Errorf("getting home directory: %w", err)
	}

	sweetfile, err := sweatfileio.LoadHierarchy(home, repoPath)
	if err != nil {
		return sweetfile, fmt.Errorf("loading sweatfile: %w", err)
	}

	return sweetfile, applyWorktreeConfig(home, sweetfile, repoPath, worktreePath)
}

// SetupFingerprint computes the current setup fingerprint for repoPath, using
// the same repo-level hierarchy (LoadHierarchy) that Create/Reapply apply, so
// the value is directly comparable to one recorded at apply time.
func SetupFingerprint(home, repoPath string) (hash string, scheme int, err error) {
	h, err := sweatfileio.LoadHierarchy(home, repoPath)
	if err != nil {
		return "", 0, fmt.Errorf("loading sweatfile: %w", err)
	}
	hash, scheme = setupfingerprint.Compute(h.Merged)
	return hash, scheme, nil
}

// SetupStale reports whether a worktree's recorded setup fingerprint/scheme is
// stale relative to the current repo config + binary + pins, with a reason.
func SetupStale(home, repoPath, recordedHash string, recordedScheme int) (stale bool, reason string, err error) {
	h, err := sweatfileio.LoadHierarchy(home, repoPath)
	if err != nil {
		return false, "", fmt.Errorf("loading sweatfile: %w", err)
	}
	stale, reason = setupfingerprint.IsStale(recordedHash, recordedScheme, h.Merged)
	return stale, reason, nil
}

// applyWorktreeConfig excludes .worktrees from git, loads and applies
// sweatfile, and trusts worktreePath in Claude. When the binary has
// a build-time-pinned madder (and/or dodder), also initialises the
// per-worktree blob store (and/or dodder repository over it) and adds
// the matching ignore + allow rules.
//
// The rule extensions are gated on the *pin*, not on whether the store
// exists — that way a user who pre-initialised it manually still gets
// the rules. Dodder runs after madder so the .default store exists for
// dodder to reuse.
func applyWorktreeConfig(
	home string,
	sweetfile sweatfile.Hierarchy,
	repoPath string,
	worktreePath string,
) error {
	if embeds.MadderBin() != "" {
		if err := madder.Init(worktreePath, embeds.MadderBin()); err != nil {
			return fmt.Errorf("initialising madder blob store: %w", err)
		}
		shimBinDir, err := spinclassShimBinDir(worktreePath)
		if err != nil {
			return fmt.Errorf("resolving spinclass shim bin dir: %w", err)
		}
		if err := madder.LinkInto(shimBinDir, embeds.MadderBin()); err != nil {
			return fmt.Errorf("linking madder into shim bin dir: %w", err)
		}
		sweetfile.Merged = withMadderEntries(sweetfile.Merged)
	}

	if embeds.DodderBin() != "" {
		if err := dodder.Init(worktreePath, embeds.DodderBin(), embeds.MadderBin()); err != nil {
			return fmt.Errorf("initialising dodder repository: %w", err)
		}
		shimBinDir, err := spinclassShimBinDir(worktreePath)
		if err != nil {
			return fmt.Errorf("resolving spinclass shim bin dir: %w", err)
		}
		if err := dodder.LinkInto(shimBinDir, embeds.DodderBin()); err != nil {
			return fmt.Errorf("linking dodder into shim bin dir: %w", err)
		}
		sweetfile.Merged = withDodderEntries(sweetfile.Merged)
	}

	merged := sweatfile.GetDefault().MergeWith(sweetfile.Merged)
	if err := applyGitExcludes(repoPath, merged.GitExcludes()); err != nil {
		return fmt.Errorf("applying git excludes: %w", err)
	}

	tmpDir := filepath.Join(worktreePath, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("creating .tmp directory: %w", err)
	}

	if err := sweetfile.Merged.Apply(worktreePath); err != nil {
		return fmt.Errorf("applying sweatfile: %w", err)
	}

	claudeJSONPath := filepath.Join(home, ".claude.json")
	if err := claude.TrustWorkspace(claudeJSONPath, worktreePath); err != nil {
		return fmt.Errorf("trusting workspace in claude: %w", err)
	}

	// Build MCP server entries from sweatfile
	var mcpEntries []claude.MCPServerEntry
	for _, mcp := range sweetfile.Merged.ActiveMCPs() {
		mcpEntries = append(mcpEntries, claude.MCPServerEntry{
			Name:    mcp.Name,
			Command: mcp.Command,
			Args:    mcp.Args,
			Env:     mcp.Env,
		})
	}

	// Auto-register dodder's MCP server when the dodder pin is active,
	// scoped to the per-worktree repository via the ceiling vars so the
	// session's agent sees this worktree's .dodder, not an ancestor's
	// (FDR 0008). A sweatfile [[mcps]] entry named "dodder" takes
	// precedence: WriteMCPConfig keys servers by name and last-wins, so
	// skip the auto entry when the user already declared one.
	if embeds.DodderBin() != "" && !slices.ContainsFunc(mcpEntries, func(e claude.MCPServerEntry) bool {
		return e.Name == "dodder"
	}) {
		mcpEntries = append(mcpEntries, claude.MCPServerEntry{
			Name:    "dodder",
			Command: embeds.DodderBin(),
			Args:    []string{"mcp"},
			Env: map[string]string{
				"DODDER_CEILING_DIRECTORIES": worktreePath,
				"MADDER_CEILING_DIRECTORIES": worktreePath,
			},
		})
	}

	if err := claude.WriteMCPConfig(worktreePath, mcpEntries); err != nil {
		return fmt.Errorf("writing .mcp.json: %w", err)
	}

	if err := sweetfile.Merged.RunCreateHook(worktreePath, os.Stdout); err != nil {
		_ = git.RunPassthrough(
			repoPath,
			"worktree",
			"remove",
			"--force",
			worktreePath,
		)
		return fmt.Errorf("create hook failed: %w", err)
	}

	// The create hook may have mutated .envrc (e.g. an inherited envrc-patch
	// hook appending directives), invalidating the `direnv allow` recorded by
	// Apply. Re-authorize the final .envrc with a bare `direnv allow` (outside
	// `direnv exec`) so the session's devshell loads unblocked. Idempotent
	// no-op when the hook left .envrc untouched or the repo has no .envrc.
	// See spinclass#213.
	if err := sweetfile.Merged.AllowDirenv(worktreePath); err != nil {
		return fmt.Errorf("re-allowing direnv after create hook: %w", err)
	}

	return nil
}

// withMadderEntries returns sf with the madder ignore + allow rules
// appended. Git/Claude sub-structs and their inner slices are cloned
// so subsequent appends here cannot clobber the caller's backing
// arrays even when cap > len.
func withMadderEntries(sf sweatfile.Sweatfile) sweatfile.Sweatfile {
	if sf.Git == nil {
		sf.Git = &sweatfile.Git{}
	} else {
		gitCopy := *sf.Git
		gitCopy.Excludes = slices.Clone(sf.Git.Excludes)
		sf.Git = &gitCopy
	}
	if !slices.Contains(sf.Git.Excludes, madder.ExcludePattern) {
		sf.Git.Excludes = append(sf.Git.Excludes, madder.ExcludePattern)
	}

	if sf.Claude == nil {
		sf.Claude = &sweatfile.Claude{}
	} else {
		claudeCopy := *sf.Claude
		claudeCopy.Allow = slices.Clone(sf.Claude.Allow)
		sf.Claude = &claudeCopy
	}
	if !slices.Contains(sf.Claude.Allow, madder.AllowRule) {
		sf.Claude.Allow = append(sf.Claude.Allow, madder.AllowRule)
	}

	return sf
}

// withDodderEntries returns sf with the dodder ignore + allow rules
// appended, cloning the Git/Claude sub-structs and their inner slices
// the same way withMadderEntries does so appends here cannot clobber the
// caller's backing arrays.
func withDodderEntries(sf sweatfile.Sweatfile) sweatfile.Sweatfile {
	if sf.Git == nil {
		sf.Git = &sweatfile.Git{}
	} else {
		gitCopy := *sf.Git
		gitCopy.Excludes = slices.Clone(sf.Git.Excludes)
		sf.Git = &gitCopy
	}
	if !slices.Contains(sf.Git.Excludes, dodder.ExcludePattern) {
		sf.Git.Excludes = append(sf.Git.Excludes, dodder.ExcludePattern)
	}

	if sf.Claude == nil {
		sf.Claude = &sweatfile.Claude{}
	} else {
		claudeCopy := *sf.Claude
		claudeCopy.Allow = slices.Clone(sf.Claude.Allow)
		sf.Claude = &claudeCopy
	}
	if !slices.Contains(sf.Claude.Allow, dodder.AllowRule) {
		sf.Claude.Allow = append(sf.Claude.Allow, dodder.AllowRule)
	}

	return sf
}

// spinclassShimBinDir is the per-repo dir that writeEnvrc adds to
// session PATH. Lives under the git common dir so all worktrees of a
// repo share one shim dir.
func spinclassShimBinDir(worktreePath string) (string, error) {
	rel, err := git.Run(worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(rel) {
		rel = filepath.Join(worktreePath, rel)
	}
	return filepath.Join(filepath.Clean(rel), "spinclass", "bin"), nil
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
