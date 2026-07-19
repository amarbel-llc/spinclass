// Package nixgc provides worktree-scoped Nix garbage collection. It enumerates
// gc roots whose link path resolves into a worktree, expands their closures,
// and attempts deletion via `nix-store --delete`. Nix's own liveness refusal
// is the safety net — paths still kept alive by other roots are reported as
// Kept rather than deleted.
package nixgc

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

// reapTimeout bounds the wall-clock duration of a single nix-store
// --delete invocation. nix-store talks to the nix-daemon over a unix
// socket and has no producer-side deadline; without this bound a stuck
// daemon would hang the auto-close prompt indefinitely (issue #68).
// Reap classifies any closure paths neither deleted nor refused at the
// deadline as Summary.TimedOut.
//
// var (not const) so tests can shorten it without waiting 30s.
var reapTimeout = 30 * time.Second

// planTimeout bounds the wall-clock duration of NewPlan's
// `nix-store --gc --print-roots` and `--query --requisites` calls
// (issue #74). Same rationale as reapTimeout: a stalled nix-daemon
// would otherwise block sc close inside planNixGC before reap is
// even reached. NewPlan returns ErrPlanTimedOut on deadline.
//
// var (not const) so tests can shorten it without waiting 30s.
var planTimeout = 30 * time.Second

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

// ErrPlanTimedOut is returned by NewPlan when one of its `nix-store`
// invocations exceeds planTimeout. Callers (close.planNixGC,
// clean.planNixGCForClean) treat this as a silent no-op so a stalled
// daemon does not break sc close / sc clean — the worktree still gets
// removed, just without the gc cleanup pass for that invocation.
var ErrPlanTimedOut = errors.New("nix-store plan step timed out")

// Disabled reports whether [hooks].disable-nix-gc is set in the sweatfile
// cascade for the given worktree. Returns false on any sweatfile-load error
// (a broken sweatfile shouldn't silently disable the feature; a real error
// would already surface elsewhere).
func Disabled(repoPath, worktreePath string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	h, err := sweatfileio.LoadWorktreeHierarchy(home, repoPath, worktreePath)
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
//
// Closure is pre-filtered against ExternallyAlive so every path it lists
// is provably unreachable from any non-worktree gc root — sidestepping
// nix-store --delete's fail-fast on the first still-rooted path. See
// issue #73 for the reasoning and the validating POC.
type Plan struct {
	WorktreePath    string
	Roots           []Root
	Closure         []string // deletable: ourClosure − ExternallyAlive, in delete-safe order (rooted paths first, deps last)
	ExternallyAlive []string // closure of all non-worktree gc roots; paths here are kept by an external root and must not be deleted
}

// Summary reports the outcome of Reap.
//
// BytesFreed is the summed NAR size of paths the daemon reported as deleted.
// BytesKept is the summed NAR size of paths it refused to delete (still
// rooted). Both are 0 when the size lookup (`nix-store -q --size`) fails or
// the size of an individual path is not available — Reap degrades to
// counts-only rather than failing.
//
// TimedOut is the count of closure paths neither deleted nor refused by
// the daemon at the ReapTimeout deadline (issue #68). Non-zero TimedOut
// means the gc made partial progress before the deadline cancelled the
// nix-store invocation; the remaining paths can be retried by an explicit
// `sc clean` once the daemon is responsive again.
type Summary struct {
	Reclaimed  int
	Kept       int
	TimedOut   int
	BytesFreed int64
	BytesKept  int64
	Errors     []error
}

// HumanFreed returns BytesFreed as a humanized string (e.g. "412.3 MiB").
func (s Summary) HumanFreed() string { return HumanizeBytes(s.BytesFreed) }

// HumanKept returns BytesKept as a humanized string.
func (s Summary) HumanKept() string { return HumanizeBytes(s.BytesKept) }

// HumanizeBytes renders n as a 1024-based human-readable size string.
// Returns "0 B" for zero, "<n> B" for sub-KiB values, and one decimal
// place at KiB / MiB / GiB / TiB / PiB scale.
func HumanizeBytes(n int64) string {
	if n < 0 {
		return "0 B"
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	v := float64(n)
	var unit string
	for _, u := range units {
		v /= 1024
		unit = u
		if v < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", v, unit)
}

// runner is the shell-out seam used by NewPlan and Reap. Tests override it.
var runner commandRunner = execRunner{}

// readLink is the symlink-resolution seam for parseRoots. Tests override it.
var readLink = os.Readlink

// lookPath is the PATH-lookup seam used by NewPlan. Tests override it
// because the nix flake-check sandbox runs without nix-store on PATH.
var lookPath = exec.LookPath

type commandRunner interface {
	// Output executes name with args under ctx and returns captured
	// stdout. NewPlan and pathSizes use this; the ctx bounds the
	// wall-clock duration so a stalled nix-daemon round-trip does not
	// block close/clean indefinitely (issues #68, #74).
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	CombinedOutput(name string, args ...string) ([]byte, error)
	// Run executes name with args under ctx, streaming stdout to outW
	// and stderr to errW. Reap uses this so callers can observe per-path
	// nix-store progress in real time (e.g. via a TAP OutputBlock writer)
	// and so a stalled nix-daemon round-trip can be cancelled by the
	// context deadline.
	Run(ctx context.Context, outW, errW io.Writer, name string, args ...string) error
}

type execRunner struct{}

func (execRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func (execRunner) CombinedOutput(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

func (execRunner) Run(ctx context.Context, outW, errW io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = outW
	cmd.Stderr = errW
	return cmd.Run()
}

// NewPlan enumerates gc roots resolving into worktreePath and expands their
// closure. Returns ErrNixUnavailable when nix-store is not on PATH and
// ErrPlanTimedOut when one of the nix-store calls exceeds planTimeout.
func NewPlan(worktreePath string) (Plan, error) {
	if _, err := lookPath("nix-store"); err != nil {
		return Plan{}, ErrNixUnavailable
	}

	abs, err := filepath.Abs(worktreePath)
	if err != nil {
		return Plan{}, fmt.Errorf("resolving worktree path: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), planTimeout)
	defer cancel()

	out, err := runner.Output(ctx, "nix-store", "--gc", "--print-roots")
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Plan{}, ErrPlanTimedOut
		}
		return Plan{}, fmt.Errorf("nix-store --print-roots: %w", err)
	}

	ourRoots, externalRoots := parseRoots(string(out), abs)
	ourClosure, err := expandClosure(ctx, ourRoots)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Plan{}, ErrPlanTimedOut
		}
		return Plan{}, fmt.Errorf("expanding worktree closure: %w", err)
	}
	externallyAlive, err := expandClosure(ctx, externalRoots)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Plan{}, ErrPlanTimedOut
		}
		return Plan{}, fmt.Errorf("expanding externally-alive closure: %w", err)
	}

	aliveSet := make(map[string]bool, len(externallyAlive))
	for _, p := range externallyAlive {
		aliveSet[p] = true
	}
	deletable := make([]string, 0, len(ourClosure))
	for _, p := range ourClosure {
		if aliveSet[p] {
			continue
		}
		deletable = append(deletable, p)
	}

	return Plan{
		WorktreePath:    abs,
		Roots:           ourRoots,
		Closure:         deletable,
		ExternallyAlive: externallyAlive,
	}, nil
}

// parseRoots parses `nix-store --gc --print-roots` output and partitions
// the entries by whether their link resolves into worktreePath. Each line is
// formatted as `<link> -> <store-path>`. Lines containing braces (e.g.
// "{censored}" markers in multi-user mode) or missing the separator are
// skipped silently. A root lands in `ours` iff its link path or one Readlink
// hop from it lands under worktreePath — covering the two common shapes:
//
//   - Auto-roots from `nix build`: link is /nix/var/nix/gcroots/auto/<hash>
//     pointing to <wt>/result.
//   - Direct roots from `nix-store --add-root <wt>/<path>`: link IS the
//     in-worktree path.
//
// Everything else parsed cleanly (including dangling auto-roots whose target
// no longer exists) lands in `external`. Its closure feeds Plan.ExternallyAlive
// so we never propose deleting a store path that some non-worktree root might
// still hold alive.
func parseRoots(output, worktreePath string) (ours, external []Root) {
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
		root := Root{LinkPath: link, StorePath: store}
		if rootInWorktree(link, worktreePath) {
			ours = append(ours, root)
		} else {
			external = append(external, root)
		}
	}
	return ours, external
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
func expandClosure(ctx context.Context, roots []Root) ([]string, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	args := []string{"--query", "--requisites"}
	for _, r := range roots {
		args = append(args, r.StorePath)
	}
	out, err := runner.Output(ctx, "nix-store", args...)
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
// that a closure of hundreds of paths would otherwise pay.
//
// Closure is pre-filtered by NewPlan to provably-dead paths (those reachable
// only from worktree-resident gc roots), so nix-store's fail-fast on the
// first still-rooted path should not trigger. If it does — e.g. a TOCTOU
// race where an external root materialized between plan and reap — the
// daemon aborts the batch on the first refusal (`gcDeleteSpecific` mode in
// nix's gc.cc throws on the first un-deletable path); the resulting Cannot-
// delete line is counted in Kept, and any closure paths neither deleted nor
// refused surface as a single error in s.Errors with the captured output
// attached for diagnosis.
//
// nix-store stdout is streamed to outW and stderr to errW so callers can
// observe progress in real time (e.g. via a TAP OutputBlock writer); pass
// io.Discard to suppress. Per-path outcomes are scanned from the captured
// output:
//
//   - lines containing "deleting '" are counted as Reclaimed
//     (nix's default-verbosity marker for each successful delete)
//   - lines matching countStillAliveRefusals are counted as Kept
//   - any closure paths not accounted for by either count are surfaced
//     as a single error in s.Errors, with the captured output attached
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

	ctx, cancel := context.WithTimeout(context.Background(), reapTimeout)
	defer cancel()

	// Capture per-path NAR sizes BEFORE delete so the post-delete summary
	// can report bytes freed (issue #58). Nil on any failure — bytes
	// reporting degrades to 0 rather than failing the gc. Shares Reap's
	// ctx so a stalled size lookup is cancelled by the same deadline as
	// the delete itself.
	sizes := pathSizes(ctx, plan.Closure)

	args := append([]string{"--delete"}, plan.Closure...)
	// nix-store streams progress to both stdout and stderr; os/exec runs
	// each pipe drain in its own goroutine, so the two MultiWriter
	// branches below race on `captured`. bytes.Buffer is not goroutine-
	// safe, so wrap it in a mutex.
	var captured syncBuffer
	err := runner.Run(
		ctx,
		io.MultiWriter(outW, &captured),
		io.MultiWriter(errW, &captured),
		"nix-store", args...,
	)
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)

	output := captured.String()
	s.Reclaimed = strings.Count(output, "deleting '")
	s.Kept = countStillAliveRefusals(output)

	if sizes != nil {
		for _, p := range extractDeletedPaths(output) {
			if sz, ok := sizes[p]; ok {
				s.BytesFreed += sz
			}
		}
		for _, p := range extractKeptPaths(output) {
			if sz, ok := sizes[p]; ok {
				s.BytesKept += sz
			}
		}
	}

	unaccounted := len(plan.Closure) - s.Reclaimed - s.Kept
	if unaccounted > 0 {
		if timedOut {
			s.TimedOut = unaccounted
		} else {
			errMsg := fmt.Errorf(
				"nix-store --delete: %d/%d path(s) not classified as deleted or kept (exit: %v): %s",
				unaccounted, len(plan.Closure), err, strings.TrimSpace(output),
			)
			s.Errors = append(s.Errors, errMsg)
		}
	}
	return s
}

// pathSizes returns a map of store path → NAR size in bytes, looked up via
// `nix-store --query --size`. Returns nil on any failure (unavailable
// command, exit error, parse error, count mismatch, ctx cancellation) so
// callers degrade to counts-only reporting.
func pathSizes(ctx context.Context, paths []string) map[string]int64 {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"--query", "--size"}, paths...)
	out, err := runner.Output(ctx, "nix-store", args...)
	if err != nil {
		return nil
	}

	sizes := make(map[string]int64, len(paths))
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	i := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if i >= len(paths) {
			return nil
		}
		sz, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return nil
		}
		sizes[paths[i]] = sz
		i++
	}
	if i != len(paths) {
		return nil
	}
	return sizes
}

// extractDeletedPaths scans nix-store --delete output for "deleting '<path>'"
// markers and returns the captured paths in order. Duplicates are preserved
// (caller dedupes if needed); typical nix output never repeats a path here.
func extractDeletedPaths(output string) []string {
	return extractQuotedPathsAfter(output, []string{"deleting '"})
}

// extractKeptPaths scans nix-store output for both refusal message styles
// (lower-case "cannot delete path '<p>'" and capitalized "Cannot delete
// path '<p>'") and returns the captured paths, deduplicated.
func extractKeptPaths(output string) []string {
	raw := extractQuotedPathsAfter(output, []string{
		"cannot delete path '",
		"Cannot delete path '",
	})
	seen := make(map[string]bool, len(raw))
	deduped := make([]string, 0, len(raw))
	for _, p := range raw {
		if seen[p] {
			continue
		}
		seen[p] = true
		deduped = append(deduped, p)
	}
	return deduped
}

// extractQuotedPathsAfter finds every occurrence of any string in `prefixes`
// followed by `<path>'` in `output` and returns the captured paths.
func extractQuotedPathsAfter(output string, prefixes []string) []string {
	var paths []string
	for _, prefix := range prefixes {
		start := 0
		for {
			idx := strings.Index(output[start:], prefix)
			if idx < 0 {
				break
			}
			abs := start + idx + len(prefix)
			closeIdx := strings.Index(output[abs:], "'")
			if closeIdx < 0 {
				break
			}
			paths = append(paths, output[abs:abs+closeIdx])
			start = abs + closeIdx + 1
		}
	}
	return paths
}

// countStillAliveRefusals tallies refusal lines in nix-store's output by
// counting occurrences of the canonical "cannot delete path" prefix, which
// appears in both message styles emitted by the daemon:
//
//   - "cannot delete path '<p>' since it is still alive" — older nix and
//     non-batch deletion paths.
//   - "Cannot delete path '<p>' because it's referenced by path '<q>'" —
//     the fail-fast batched-delete refusal in current nix (gc.cc's
//     gcDeleteSpecific mode), which aborts the batch on the first such
//     path. With NewPlan's pre-filter this should not fire; if it does,
//     it indicates a TOCTOU race or a partition bug and the count is
//     surfaced via Reap's Errors path.
func countStillAliveRefusals(output string) int {
	return strings.Count(strings.ToLower(output), "cannot delete path")
}

func isStillAliveRefusal(output string) bool {
	return strings.Contains(strings.ToLower(output), "cannot delete path")
}
