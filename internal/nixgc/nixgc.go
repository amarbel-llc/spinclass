// Package nixgc provides worktree-scoped Nix garbage collection. It enumerates
// gc roots whose link path resolves into a worktree, expands their closures,
// and attempts deletion via `nix-store --delete`. Nix's own liveness refusal
// is the safety net — paths still kept alive by other roots are reported as
// Kept rather than deleted.
package nixgc

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

// syncBuffer is a goroutine-safe wrapper around bytes.Buffer. Reap
// hands the same buffer to two MultiWriters (stdout and stderr) so it
// can scan the combined output for "deleting '" / "still alive" markers
// later; os/exec drains the pipes in separate goroutines, so writes
// must be serialized.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

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
	// Run executes name with args, streaming stdout to outW and stderr
	// to errW. Reap uses this so callers can observe per-path nix-store
	// progress in real time (e.g. via a TAP OutputBlock writer).
	Run(outW, errW io.Writer, name string, args ...string) error
}

type execRunner struct{}

func (execRunner) Output(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

func (execRunner) CombinedOutput(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

func (execRunner) Run(outW, errW io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = outW
	cmd.Stderr = errW
	return cmd.Run()
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

// Reap deletes plan.Closure with a single `nix-store --delete <p1> <p2>
// ...` invocation. Batching avoids the per-path fork+exec+daemon-RTT cost
// that a closure of hundreds of paths would otherwise pay; nix's daemon
// processes the list in one connection and continues past per-path "still
// alive" refusals.
//
// nix-store stdout is streamed to outW and stderr to errW so callers can
// observe progress in real time (e.g. via a TAP OutputBlock writer); pass
// io.Discard to suppress. The output is also captured internally and
// scanned to classify per-path outcomes:
//
//   - lines containing "deleting '" are counted as Reclaimed
//     (nix's default-verbosity marker for each successful delete)
//   - lines matching countStillAliveRefusals are counted as Kept
//   - any closure paths not accounted for by either count are surfaced
//     as a single error in s.Errors, with the captured output attached
//     for diagnosis
//
// Nil writers are treated as io.Discard. An empty closure is a no-op.
func Reap(plan Plan, outW, errW io.Writer) Summary {
	var s Summary
	if outW == nil {
		outW = io.Discard
	}
	if errW == nil {
		errW = io.Discard
	}
	if len(plan.Closure) == 0 {
		return s
	}

	args := append([]string{"--delete"}, plan.Closure...)
	// nix-store streams progress to both stdout and stderr; os/exec runs
	// each pipe drain in its own goroutine, so the two MultiWriter
	// branches below race on `captured`. bytes.Buffer is not goroutine-
	// safe, so wrap it in a mutex.
	var captured syncBuffer
	err := runner.Run(
		io.MultiWriter(outW, &captured),
		io.MultiWriter(errW, &captured),
		"nix-store", args...,
	)

	output := captured.String()
	s.Reclaimed = strings.Count(output, "deleting '")
	s.Kept = countStillAliveRefusals(output)

	unaccounted := len(plan.Closure) - s.Reclaimed - s.Kept
	if unaccounted > 0 {
		errMsg := fmt.Errorf(
			"nix-store --delete: %d/%d path(s) not classified as deleted or kept (exit: %v): %s",
			unaccounted, len(plan.Closure), err, strings.TrimSpace(output),
		)
		s.Errors = append(s.Errors, errMsg)
	}
	return s
}

// countStillAliveRefusals tallies "cannot delete path '<p>' since it is
// still alive" (and the related "still in use" / "is in use") error
// lines in nix-store's stderr — one per refused path.
func countStillAliveRefusals(output string) int {
	lower := strings.ToLower(output)
	return strings.Count(lower, "still alive") +
		strings.Count(lower, "still in use") +
		strings.Count(lower, "is in use")
}

func isStillAliveRefusal(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "still alive") ||
		strings.Contains(lower, "still in use") ||
		strings.Contains(lower, "is in use")
}
