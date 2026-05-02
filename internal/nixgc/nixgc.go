// Package nixgc provides worktree-scoped Nix garbage collection. It enumerates
// gc roots whose link path resolves into a worktree, expands their closures,
// and attempts deletion via `nix-store --delete`. Nix's own liveness refusal
// is the safety net — paths still kept alive by other roots are reported as
// Kept rather than deleted.
package nixgc

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

// ErrNixUnavailable is returned by NewPlan when `nix-store` is not on PATH.
// Callers should treat this as a silent no-op.
var ErrNixUnavailable = errors.New("nix-store not on PATH")

// Disabled reports whether [hooks].disable-nix-gc is set in the sweatfile
// cascade for the given worktree. Returns false on any sweatfile-load error
// (a broken sweatfile shouldn't silently disable the feature; a real error
// would already surface elsewhere).
func Disabled(repoPath, worktreePath string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	h, err := sweatfile.LoadWorktreeHierarchy(home, repoPath, worktreePath)
	if err != nil {
		return false
	}
	return h.Merged.DisableNixGCEnabled()
}

// Root is a gc root whose symlink chain resolves into the worktree.
type Root struct {
	LinkPath  string // e.g. /nix/var/nix/gcroots/auto/abc123
	StorePath string // e.g. /nix/store/...-foo
}

// Plan enumerates the worktree's gc roots and their closure.
type Plan struct {
	WorktreePath string
	Roots        []Root
	Closure      []string // store paths in delete-safe order (rooted paths first, deps last)
}

// Summary reports the outcome of Reap.
type Summary struct {
	Reclaimed int
	Kept      int
	Errors    []error
}

// runner is the shell-out seam used by NewPlan and Reap. Tests override it.
var runner commandRunner = execRunner{}

// readLink is the symlink-resolution seam for parseRoots. Tests override it.
var readLink = os.Readlink

type commandRunner interface {
	Output(name string, args ...string) ([]byte, error)
	CombinedOutput(name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Output(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

func (execRunner) CombinedOutput(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// NewPlan enumerates gc roots resolving into worktreePath and expands their
// closure. Returns ErrNixUnavailable when nix-store is not on PATH.
func NewPlan(worktreePath string) (Plan, error) {
	if _, err := exec.LookPath("nix-store"); err != nil {
		return Plan{}, ErrNixUnavailable
	}

	abs, err := filepath.Abs(worktreePath)
	if err != nil {
		return Plan{}, fmt.Errorf("resolving worktree path: %w", err)
	}

	out, err := runner.Output("nix-store", "--gc", "--print-roots")
	if err != nil {
		return Plan{}, fmt.Errorf("nix-store --print-roots: %w", err)
	}

	roots := parseRoots(string(out), abs)
	closure, err := expandClosure(roots)
	if err != nil {
		return Plan{}, fmt.Errorf("expanding closure: %w", err)
	}

	return Plan{
		WorktreePath: abs,
		Roots:        roots,
		Closure:      closure,
	}, nil
}

// parseRoots parses `nix-store --gc --print-roots` output. Each line is
// formatted as `<link> -> <store-path>`. Lines containing braces (e.g.
// "{censored}" markers in multi-user mode) or missing the separator are
// skipped silently. A root is included iff its link path or one Readlink hop
// from it lands under worktreePath — covering the two common shapes:
//
//   - Auto-roots from `nix build`: link is /nix/var/nix/gcroots/auto/<hash>
//     pointing to <wt>/result.
//   - Direct roots from `nix-store --add-root <wt>/<path>`: link IS the
//     in-worktree path.
func parseRoots(output, worktreePath string) []Root {
	var roots []Root
	sc := bufio.NewScanner(strings.NewReader(output))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.Contains(line, "{") {
			continue
		}
		idx := strings.Index(line, " -> ")
		if idx < 0 {
			continue
		}
		link := strings.TrimSpace(line[:idx])
		store := strings.TrimSpace(line[idx+len(" -> "):])
		if link == "" || store == "" {
			continue
		}
		if !rootInWorktree(link, worktreePath) {
			continue
		}
		roots = append(roots, Root{LinkPath: link, StorePath: store})
	}
	return roots
}

// rootInWorktree reports whether link (or its single readlink target)
// resolves under worktreePath. One hop is enough for the cases we care about
// (auto-roots, direct add-root); deeper chains would be unusual and are
// intentionally not followed to keep behavior predictable.
func rootInWorktree(link, worktreePath string) bool {
	if pathInDir(link, worktreePath) {
		return true
	}
	target, err := readLink(link)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(link), target)
	}
	return pathInDir(filepath.Clean(target), worktreePath)
}

func pathInDir(path, dir string) bool {
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

// expandClosure runs `nix-store --query --requisites` for each root's store
// path, returning a deduplicated list of paths in delete-safe order: the
// rooted paths first, then their dependencies. `--requisites` prints deps
// first, the path itself last; we reverse and dedupe.
func expandClosure(roots []Root) ([]string, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	args := []string{"--query", "--requisites"}
	for _, r := range roots {
		args = append(args, r.StorePath)
	}
	out, err := runner.Output("nix-store", args...)
	if err != nil {
		return nil, err
	}

	var ordered []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		path := strings.TrimSpace(sc.Text())
		if path == "" {
			continue
		}
		ordered = append(ordered, path)
	}

	// Reverse to put the rooted path before its deps, then dedupe so the
	// first occurrence (highest in the graph) wins.
	seen := make(map[string]bool, len(ordered))
	out2 := make([]string, 0, len(ordered))
	for i := len(ordered) - 1; i >= 0; i-- {
		p := ordered[i]
		if seen[p] {
			continue
		}
		seen[p] = true
		out2 = append(out2, p)
	}
	return out2, nil
}

// Reap iterates plan.Closure and attempts `nix-store --delete` on each path.
// Nix refuses paths still kept alive elsewhere; those are counted as Kept.
// Other failures accumulate in Errors but iteration continues.
func Reap(plan Plan) Summary {
	var s Summary
	for _, path := range plan.Closure {
		out, err := runner.CombinedOutput("nix-store", "--delete", path)
		if err == nil {
			s.Reclaimed++
			continue
		}
		if isStillAliveRefusal(string(out)) {
			s.Kept++
			continue
		}
		s.Errors = append(
			s.Errors,
			fmt.Errorf("delete %s: %w: %s", path, err, strings.TrimSpace(string(out))),
		)
	}
	return s
}

func isStillAliveRefusal(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "still alive") ||
		strings.Contains(lower, "still in use") ||
		strings.Contains(lower, "is in use")
}
