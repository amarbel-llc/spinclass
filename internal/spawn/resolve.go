package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveRepo resolves a spawn target. A target containing a path separator
// or pointing at an existing path is used directly (escape hatch);
// otherwise it is a repo dirname, leaf-searched as $HOME/*/repos/<leaf>
// (FDR 0006: leaves are unique across workspace roots; a configured root
// list is deferred). The resolved dir must be a git repo whose .git is a
// directory (a main checkout, not a worktree), and must be a DIFFERENT repo
// than the driver's — detached fork covers the same-repo case.
func ResolveRepo(home, target, driverRepoPath string) (string, error) {
	if strings.ContainsRune(target, os.PathSeparator) || pathExists(target) {
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", fmt.Errorf("resolving spawn target %q: %w", target, err)
		}
		if !isMainCheckout(abs) {
			return "", fmt.Errorf(
				"spawn target %q is not a git main checkout (.git must be a directory — worktrees cannot be spawn targets)", abs,
			)
		}
		return abs, rejectDriverRepo(abs, driverRepoPath)
	}

	candidates, err := filepath.Glob(filepath.Join(home, "*", "repos", target))
	if err != nil {
		return "", fmt.Errorf("searching workspace roots for %q: %w", target, err)
	}
	var repos []string
	for _, c := range candidates {
		if isMainCheckout(c) {
			repos = append(repos, c)
		}
	}
	switch len(repos) {
	case 0:
		return "", fmt.Errorf(
			"no repo named %q found under %s (searched %s)", target, home, filepath.Join(home, "*", "repos", target),
		)
	case 1:
		return repos[0], rejectDriverRepo(repos[0], driverRepoPath)
	default:
		return "", fmt.Errorf(
			"repo dirname %q is ambiguous across workspace roots: %s (use an explicit path)",
			target, strings.Join(repos, ", "),
		)
	}
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// isMainCheckout reports whether dir is a git repo whose .git is a
// directory — a main checkout, not a worktree (those have a .git file).
func isMainCheckout(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

// rejectDriverRepo errors when resolved is the driver's own repo: spawn
// targets a sibling repo by contract (FDR 0006); a worker on a branch of
// the driver's repo is the detached-fork path.
func rejectDriverRepo(resolved, driverRepoPath string) error {
	if canonical(resolved) == canonical(driverRepoPath) {
		return fmt.Errorf(
			"spawn target %s is the driver's own repo — spawn launches workers in a different repo; use a detached fork for a worker on a branch of this one", resolved,
		)
	}
	return nil
}

// canonical best-effort symlink-resolves p for comparison; falls back to
// the cleaned path when resolution fails.
func canonical(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}
