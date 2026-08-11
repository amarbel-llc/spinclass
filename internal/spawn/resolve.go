package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveRepo resolves a spawn target. A target containing a path separator
// is used directly as a path (escape hatch — a bare name NEVER is, even when
// it matches a cwd subdir, so leaf search stays deterministic regardless of
// where the driver runs); otherwise it is a repo dirname, leaf-searched as
// $HOME/*/repos/<leaf> (FDR 0006: leaves are unique across workspace roots;
// a configured root list is deferred). The resolved dir must be a git repo
// whose .git is a directory (a main checkout, not a worktree). The target MAY
// be the driver's own repo: spinclass#262 unified spawn/fork, so a spawn into
// the current repo is a fresh worker off its default branch (the caller passes
// the current repo when `repo` is omitted); there is no same-repo refusal.
func ResolveRepo(home, target string) (string, error) {
	if strings.ContainsRune(target, filepath.Separator) || strings.Contains(target, "/") {
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", fmt.Errorf("resolving spawn target %q: %w", target, err)
		}
		if !isMainCheckout(abs) {
			return "", fmt.Errorf(
				"spawn target %q is not a git main checkout (.git must be a directory — worktrees cannot be spawn targets)", abs,
			)
		}
		return abs, nil
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
		return repos[0], nil
	default:
		return "", fmt.Errorf(
			"repo dirname %q is ambiguous across workspace roots: %s (use an explicit path)",
			target, strings.Join(repos, ", "),
		)
	}
}

// isMainCheckout reports whether dir is a git repo whose .git is a
// directory — a main checkout, not a worktree (those have a .git file).
func isMainCheckout(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}
