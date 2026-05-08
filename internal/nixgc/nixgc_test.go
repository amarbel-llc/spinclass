package nixgc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestParseRootsHappy(t *testing.T) {
	wt := "/home/u/eng/wt/snug-aspen"
	out := `/nix/var/nix/gcroots/auto/abc -> /nix/store/aaa-foo
/nix/var/nix/gcroots/auto/def -> /nix/store/bbb-bar
{censored}
/nix/var/nix/profiles/system -> /nix/store/ccc-system
`
	defer overrideReadLink(map[string]string{
		"/nix/var/nix/gcroots/auto/abc": "/home/u/eng/wt/snug-aspen/result",
		"/nix/var/nix/gcroots/auto/def": "/home/u/eng/other/result",
		"/nix/var/nix/profiles/system":  "/nix/var/nix/profiles/system-1-link",
	})()

	ours, external := parseRoots(out, wt)
	wantOurs := []Root{
		{LinkPath: "/nix/var/nix/gcroots/auto/abc", StorePath: "/nix/store/aaa-foo"},
	}
	wantExternal := []Root{
		{LinkPath: "/nix/var/nix/gcroots/auto/def", StorePath: "/nix/store/bbb-bar"},
		{LinkPath: "/nix/var/nix/profiles/system", StorePath: "/nix/store/ccc-system"},
	}
	if !reflect.DeepEqual(ours, wantOurs) {
		t.Errorf("parseRoots ours = %v, want %v", ours, wantOurs)
	}
	if !reflect.DeepEqual(external, wantExternal) {
		t.Errorf("parseRoots external = %v, want %v", external, wantExternal)
	}
}

func TestParseRootsLinkInsideWorktree(t *testing.T) {
	// Direct gc root where the link itself lives in the worktree (e.g.
	// `nix-store --add-root <wt>/.gcroot/foo`).
	wt := "/home/u/wt"
	out := "/home/u/wt/.gcroot/foo -> /nix/store/aaa-foo\n"
	defer overrideReadLink(nil)()
	ours, external := parseRoots(out, wt)
	if len(ours) != 1 || ours[0].LinkPath != "/home/u/wt/.gcroot/foo" {
		t.Errorf("expected in-worktree link to match without readlink, got %v", ours)
	}
	if len(external) != 0 {
		t.Errorf("expected no external roots, got %v", external)
	}
}

func TestParseRootsDanglingSymlink(t *testing.T) {
	wt := "/home/u/eng/wt/snug-aspen"
	out := "/nix/var/nix/gcroots/auto/abc -> /nix/store/aaa-foo\n"
	// Empty resolver → readLink returns ENOENT-ish error → not in worktree.
	defer overrideReadLink(nil)()
	ours, external := parseRoots(out, wt)
	if len(ours) != 0 {
		t.Errorf("expected dangling-link root not to land in ours, got %v", ours)
	}
	// A dangling auto-root still represents a gcroot we don't own; its
	// store path may or may not be alive but we conservatively treat it
	// as externally rooted so its closure is excluded from deletable.
	if len(external) != 1 || external[0].StorePath != "/nix/store/aaa-foo" {
		t.Errorf("expected dangling-link root to land in external, got %v", external)
	}
}

func TestParseRootsCensoredAndMalformed(t *testing.T) {
	wt := "/home/u/wt"
	out := `{censored}
no-arrow-here
/empty-link ->
-> /nix/store/x
`
	defer overrideReadLink(nil)()
	ours, external := parseRoots(out, wt)
	if len(ours) != 0 || len(external) != 0 {
		t.Errorf("expected all malformed lines to be skipped, got ours=%v external=%v", ours, external)
	}
}

func TestParseRootsExactWorktreeMatch(t *testing.T) {
	wt := "/home/u/wt"
	out := "/nix/var/nix/gcroots/auto/abc -> /nix/store/aaa\n"
	defer overrideReadLink(map[string]string{
		"/nix/var/nix/gcroots/auto/abc": "/home/u/wt",
	})()
	ours, _ := parseRoots(out, wt)
	if len(ours) != 1 {
		t.Fatalf("expected 1 root for exact-match worktree path, got %v", ours)
	}
}

func TestParseRootsRelativeSymlinkTarget(t *testing.T) {
	wt := "/home/u/wt"
	out := "/home/u/wt/.gcroot/x -> /nix/store/aaa\n"
	// Link is already in worktree → matches without readlink. Verifies the
	// in-worktree fast path doesn't depend on the resolver.
	defer overrideReadLink(map[string]string{})()
	ours, _ := parseRoots(out, wt)
	if len(ours) != 1 {
		t.Errorf("expected in-worktree link to match, got %v", ours)
	}
}

func TestIsStillAliveRefusal(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"error: cannot delete path '/nix/store/abc' since it is still alive", true},
		// Fail-fast batched-delete refusal (current nix master / Determinate
		// 3.15.2). Pre-filter should normally prevent this from firing.
		{"error: Cannot delete path '/nix/store/aaa-numactl' because it's referenced by path '/nix/store/bbb-lttng'", true},
		{"network unreachable", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isStillAliveRefusal(c.in); got != c.want {
			t.Errorf("isStillAliveRefusal(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestExpandClosureReverses(t *testing.T) {
	roots := []Root{{StorePath: "/nix/store/top"}}
	defer overrideRunner(stubRunner{output: []byte("/nix/store/dep-a\n/nix/store/dep-b\n/nix/store/top\n")})()

	got, err := expandClosure(roots)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/nix/store/top", "/nix/store/dep-b", "/nix/store/dep-a"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expandClosure ordering = %v, want %v (top first, deps after)", got, want)
	}
}

func TestExpandClosureDedupes(t *testing.T) {
	roots := []Root{
		{StorePath: "/nix/store/a"},
		{StorePath: "/nix/store/b"},
	}
	defer overrideRunner(stubRunner{output: []byte("/nix/store/shared\n/nix/store/a\n/nix/store/shared\n/nix/store/b\n")})()

	got, err := expandClosure(roots)
	if err != nil {
		t.Fatal(err)
	}
	sortedGot := append([]string(nil), got...)
	sort.Strings(sortedGot)
	want := []string{"/nix/store/a", "/nix/store/b", "/nix/store/shared"}
	if !reflect.DeepEqual(sortedGot, want) {
		t.Errorf("expandClosure dedup = %v (sorted), want %v", sortedGot, want)
	}
}

func TestReapTallies(t *testing.T) {
	plan := Plan{Closure: []string{
		"/nix/store/ok",
		"/nix/store/alive",
		"/nix/store/broken",
	}}
	// Reap now batches the entire closure into a single nix-store
	// invocation; the stub returns the combined stderr for that one call.
	combinedStderr := strings.Join([]string{
		"deleting '/nix/store/ok'",
		"error: cannot delete path '/nix/store/alive' since it is still alive",
		"error: some other failure for /nix/store/broken",
		"",
	}, "\n")
	defer overrideRunner(stubRunner{
		output: []byte(combinedStderr),
		err:    errors.New("exit status 1"),
	})()

	var streamed bytes.Buffer
	s := Reap(plan, &streamed, &streamed)
	if s.Reclaimed != 1 {
		t.Errorf("Reclaimed = %d, want 1", s.Reclaimed)
	}
	if s.Kept != 1 {
		t.Errorf("Kept = %d, want 1", s.Kept)
	}
	// "broken" was neither deleted nor still-alive — it lands in Errors
	// as a single batched entry describing how many paths went unclassified.
	if len(s.Errors) != 1 {
		t.Errorf("Errors = %v, want 1 entry", s.Errors)
	}

	// Combined nix-store output should reach the stream writer so callers
	// can show real-time progress / diagnose hangs.
	got := streamed.String()
	for _, want := range []string{"deleting '/nix/store/ok'", "still alive", "some other failure"} {
		if !strings.Contains(got, want) {
			t.Errorf("streamed output missing %q; got %q", want, got)
		}
	}
}

func TestReapEmptyClosureIsNoop(t *testing.T) {
	// With no paths to delete, Reap must not invoke nix-store at all
	// (the runner is set to one that errors on any call to make this
	// observable).
	defer overrideRunner(stubRunner{err: errors.New("nix-store should not be called")})()

	s := Reap(Plan{}, nil, nil)
	if s.Reclaimed != 0 || s.Kept != 0 || len(s.Errors) != 0 {
		t.Errorf("expected empty Summary for empty closure, got %+v", s)
	}
}

func TestReapAllSucceeded(t *testing.T) {
	plan := Plan{Closure: []string{
		"/nix/store/a",
		"/nix/store/b",
	}}
	combinedStderr := "deleting '/nix/store/a'\ndeleting '/nix/store/b'\n"
	defer overrideRunner(stubRunner{output: []byte(combinedStderr)})()

	s := Reap(plan, nil, nil)
	if s.Reclaimed != 2 {
		t.Errorf("Reclaimed = %d, want 2", s.Reclaimed)
	}
	if s.Kept != 0 {
		t.Errorf("Kept = %d, want 0", s.Kept)
	}
	if len(s.Errors) != 0 {
		t.Errorf("Errors = %v, want none", s.Errors)
	}
}

// TestNewPlanFiltersExternallyAlive is the regression test for issue #73's
// sharp-fir shape: a path appears in both the worktree's closure and an
// external root's closure. NewPlan must drop it from Plan.Closure so the
// subsequent `nix-store --delete` cannot fail-fast on it.
func TestNewPlanFiltersExternallyAlive(t *testing.T) {
	wt := "/home/u/wt"
	rootsOut := "/home/u/wt/result -> /nix/store/ours-app\n" +
		"/nix/var/nix/profiles/system -> /nix/store/sys\n"

	// expandClosure scans the requisites output and reverses it (top-first,
	// deps-last). With these inputs:
	//   reverse(ourReq) = [ours-app, dep-private, dep-shared]
	//   reverse(extReq) = [sys, sys-glibc, dep-shared]
	// dep-shared appears in both, so the filter must drop it.
	ourReq := "/nix/store/dep-shared\n/nix/store/dep-private\n/nix/store/ours-app\n"
	extReq := "/nix/store/dep-shared\n/nix/store/sys-glibc\n/nix/store/sys\n"

	defer overrideLookPath(true)()
	defer overrideReadLink(map[string]string{
		"/nix/var/nix/profiles/system": "/nix/var/nix/profiles/system-1-link",
	})()
	sr := &sequencedRunner{outputs: [][]byte{
		[]byte(rootsOut),
		[]byte(ourReq),
		[]byte(extReq),
	}}
	defer overrideRunner(sr)()

	plan, err := NewPlan(wt)
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.Roots) != 1 || plan.Roots[0].StorePath != "/nix/store/ours-app" {
		t.Errorf("Roots = %v, want one ours-app root", plan.Roots)
	}

	wantAlive := []string{"/nix/store/sys", "/nix/store/sys-glibc", "/nix/store/dep-shared"}
	if !reflect.DeepEqual(plan.ExternallyAlive, wantAlive) {
		t.Errorf("ExternallyAlive = %v, want %v", plan.ExternallyAlive, wantAlive)
	}

	wantClosure := []string{"/nix/store/ours-app", "/nix/store/dep-private"}
	if !reflect.DeepEqual(plan.Closure, wantClosure) {
		t.Errorf("Closure = %v, want %v (dep-shared must be filtered out)", plan.Closure, wantClosure)
	}

	if sr.calls != 3 {
		t.Errorf("runner.Output calls = %d, want 3 (print-roots + 2 requisites queries)", sr.calls)
	}
}

// TestNewPlanNoExternalRootsSkipsRequisitesCall verifies the no-external
// short-circuit: when every parsed root is in the worktree, NewPlan must
// not invoke `nix-store --query --requisites` for an empty external set.
func TestNewPlanNoExternalRootsSkipsRequisitesCall(t *testing.T) {
	wt := "/home/u/wt"
	rootsOut := "/home/u/wt/result -> /nix/store/ours-app\n"
	ourReq := "/nix/store/dep\n/nix/store/ours-app\n"

	defer overrideLookPath(true)()
	defer overrideReadLink(nil)()
	sr := &sequencedRunner{outputs: [][]byte{
		[]byte(rootsOut),
		[]byte(ourReq),
	}}
	defer overrideRunner(sr)()

	plan, err := NewPlan(wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ExternallyAlive) != 0 {
		t.Errorf("ExternallyAlive = %v, want empty", plan.ExternallyAlive)
	}
	if sr.calls != 2 {
		t.Errorf("runner.Output calls = %d, want 2 (no requisites query for empty external set)", sr.calls)
	}
}

// --- helpers -----------------------------------------------------------------

// overrideReadLink swaps the package-level readLink seam for the duration of
// a test. Pass a map of link → target; missing keys return a "not found"
// error (so links not present in the map look "dangling" to parseRoots).
// Returns a teardown closure suitable for `defer overrideReadLink(...)()`.
func overrideReadLink(table map[string]string) func() {
	old := readLink
	readLink = func(name string) (string, error) {
		if target, ok := table[name]; ok {
			return target, nil
		}
		return "", errSymlinkNotFound
	}
	return func() { readLink = old }
}

func overrideRunner(r commandRunner) func() {
	old := runner
	runner = r
	return func() { runner = old }
}

// overrideLookPath swaps the package-level lookPath seam. Tests use this
// to bypass the `exec.LookPath("nix-store")` guard in NewPlan because
// the nix flake-check sandbox runs without nix-store on PATH.
func overrideLookPath(found bool) func() {
	old := lookPath
	if found {
		lookPath = func(string) (string, error) { return "/usr/bin/nix-store", nil }
	} else {
		lookPath = func(string) (string, error) { return "", errors.New("test: nix-store not on PATH") }
	}
	return func() { lookPath = old }
}

var errSymlinkNotFound = errors.New("test: symlink not found")

type stubRunner struct {
	output []byte
	err    error
}

func (s stubRunner) Output(_ string, _ ...string) ([]byte, error) {
	return s.output, s.err
}

func (s stubRunner) CombinedOutput(_ string, _ ...string) ([]byte, error) {
	return s.output, s.err
}

func (s stubRunner) Run(_ context.Context, outW, _ io.Writer, _ string, _ ...string) error {
	if outW != nil && len(s.output) > 0 {
		outW.Write(s.output)
	}
	return s.err
}

// sequencedRunner returns predefined outputs in call order. Used by
// TestNewPlanFiltersExternallyAlive to distinguish the three Output
// invocations NewPlan makes: --gc --print-roots, --query --requisites
// for the worktree's roots, and --query --requisites for external roots.
type sequencedRunner struct {
	outputs [][]byte
	errs    []error
	calls   int
}

func (s *sequencedRunner) Output(_ string, args ...string) ([]byte, error) {
	if s.calls >= len(s.outputs) {
		s.calls++
		return nil, fmt.Errorf("unexpected runner call #%d args=%v", s.calls, args)
	}
	out := s.outputs[s.calls]
	var err error
	if s.calls < len(s.errs) {
		err = s.errs[s.calls]
	}
	s.calls++
	return out, err
}

func (s *sequencedRunner) CombinedOutput(name string, args ...string) ([]byte, error) {
	return s.Output(name, args...)
}

func (s *sequencedRunner) Run(_ context.Context, _, _ io.Writer, _ string, _ ...string) error {
	return errors.New("sequencedRunner.Run should not be invoked")
}

// reapStub returns separate outputs for Output (size query) vs Run
// (delete invocation), matching the two-call shape Reap exercises after
// issue #58. Use sizesOutput for `nix-store --query --size` and
// runOutput as the streamed delete output.
type reapStub struct {
	sizesOutput []byte
	sizesErr    error
	runOutput   []byte
	runErr      error
}

func (r reapStub) Output(_ string, _ ...string) ([]byte, error) {
	return r.sizesOutput, r.sizesErr
}

func (r reapStub) CombinedOutput(_ string, _ ...string) ([]byte, error) {
	return r.runOutput, r.runErr
}

func (r reapStub) Run(_ context.Context, outW, _ io.Writer, _ string, _ ...string) error {
	if outW != nil && len(r.runOutput) > 0 {
		outW.Write(r.runOutput)
	}
	return r.runErr
}

func TestHumanizeBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{412 * 1024 * 1024, "412.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
		// 2.5 GiB built from integer arithmetic to avoid untyped-float→int64 conversion.
		{(5 * 1024 * 1024 * 1024) / 2, "2.5 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TiB"},
		{-1, "0 B"},
	}
	for _, c := range cases {
		if got := HumanizeBytes(c.in); got != c.want {
			t.Errorf("HumanizeBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractDeletedPaths(t *testing.T) {
	output := strings.Join([]string{
		"deleting '/nix/store/aaa-foo'",
		"deleting '/nix/store/bbb-bar'",
		"some other line",
		"deleting '/nix/store/ccc-baz'",
	}, "\n")
	got := extractDeletedPaths(output)
	want := []string{"/nix/store/aaa-foo", "/nix/store/bbb-bar", "/nix/store/ccc-baz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractDeletedPaths = %v, want %v", got, want)
	}
}

func TestExtractKeptPathsBothStyles(t *testing.T) {
	output := strings.Join([]string{
		"error: cannot delete path '/nix/store/lower-style' since it is still alive",
		"error: Cannot delete path '/nix/store/upper-style' because it's referenced by path '/nix/store/q'",
		// Same path repeated under both styles must dedupe.
		"error: cannot delete path '/nix/store/dup' since it is still alive",
		"error: Cannot delete path '/nix/store/dup' because it's referenced",
	}, "\n")
	got := extractKeptPaths(output)
	want := []string{"/nix/store/lower-style", "/nix/store/dup", "/nix/store/upper-style"}
	if !reflect.DeepEqual(sortedCopy(got), sortedCopy(want)) {
		t.Errorf("extractKeptPaths = %v, want (any order) %v", got, want)
	}
}

func TestPathSizesParsesLines(t *testing.T) {
	defer overrideRunner(stubRunner{output: []byte("100\n2048\n3145728\n")})()

	got := pathSizes([]string{"/nix/store/a", "/nix/store/b", "/nix/store/c"})
	want := map[string]int64{
		"/nix/store/a": 100,
		"/nix/store/b": 2048,
		"/nix/store/c": 3145728,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pathSizes = %v, want %v", got, want)
	}
}

func TestPathSizesReturnsNilOnNonNumericLine(t *testing.T) {
	defer overrideRunner(stubRunner{output: []byte("100\nNaN\n300\n")})()

	if got := pathSizes([]string{"/nix/store/a", "/nix/store/b", "/nix/store/c"}); got != nil {
		t.Errorf("pathSizes should return nil on parse failure, got %v", got)
	}
}

func TestPathSizesReturnsNilOnLineCountMismatch(t *testing.T) {
	defer overrideRunner(stubRunner{output: []byte("100\n200\n")})()

	if got := pathSizes([]string{"/nix/store/a", "/nix/store/b", "/nix/store/c"}); got != nil {
		t.Errorf("pathSizes should return nil when output has fewer lines than paths, got %v", got)
	}
}

func TestPathSizesReturnsNilOnRunnerError(t *testing.T) {
	defer overrideRunner(stubRunner{err: errors.New("boom")})()

	if got := pathSizes([]string{"/nix/store/a"}); got != nil {
		t.Errorf("pathSizes should return nil on runner error, got %v", got)
	}
}

func TestReapBytesFreedAccountsForDeletedAndKept(t *testing.T) {
	plan := Plan{Closure: []string{
		"/nix/store/big-deleted",
		"/nix/store/small-deleted",
		"/nix/store/kept",
	}}
	defer overrideRunner(reapStub{
		// Sizes in plan order: big=10MiB, small=1KiB, kept=4MiB.
		sizesOutput: []byte("10485760\n1024\n4194304\n"),
		runOutput: []byte(strings.Join([]string{
			"deleting '/nix/store/big-deleted'",
			"deleting '/nix/store/small-deleted'",
			"error: cannot delete path '/nix/store/kept' since it is still alive",
			"",
		}, "\n")),
	})()

	s := Reap(plan, nil, nil)
	if s.Reclaimed != 2 {
		t.Errorf("Reclaimed = %d, want 2", s.Reclaimed)
	}
	if s.Kept != 1 {
		t.Errorf("Kept = %d, want 1", s.Kept)
	}
	wantFreed := int64(10485760 + 1024)
	if s.BytesFreed != wantFreed {
		t.Errorf("BytesFreed = %d, want %d", s.BytesFreed, wantFreed)
	}
	if s.BytesKept != int64(4194304) {
		t.Errorf("BytesKept = %d, want %d", s.BytesKept, 4194304)
	}
	if s.HumanFreed() != "10.0 MiB" {
		t.Errorf("HumanFreed = %q, want %q", s.HumanFreed(), "10.0 MiB")
	}
}

func TestReapBytesFreedDegradesWhenSizesUnavailable(t *testing.T) {
	plan := Plan{Closure: []string{"/nix/store/a"}}
	defer overrideRunner(reapStub{
		sizesErr:  errors.New("nix-store unavailable"),
		runOutput: []byte("deleting '/nix/store/a'\n"),
	})()

	s := Reap(plan, nil, nil)
	if s.Reclaimed != 1 {
		t.Errorf("Reclaimed = %d, want 1", s.Reclaimed)
	}
	if s.BytesFreed != 0 {
		t.Errorf("BytesFreed = %d, want 0 (degraded), got non-zero", s.BytesFreed)
	}
}

func sortedCopy(s []string) []string {
	cp := append([]string(nil), s...)
	sort.Strings(cp)
	return cp
}

// hangingRunner emits partial output and then blocks Run until the
// caller's context fires. Used with a shortened reapTimeout to drive
// Reap's timeout-classification path without waiting the real 30s.
type hangingRunner struct {
	partial []byte
}

func (h hangingRunner) Output(_ string, _ ...string) ([]byte, error) {
	// Decline size lookup so pathSizes degrades to nil; bytes accounting
	// is not under test here.
	return nil, errors.New("size-lookup declined for hangingRunner")
}

func (h hangingRunner) CombinedOutput(_ string, _ ...string) ([]byte, error) {
	return nil, nil
}

func (h hangingRunner) Run(ctx context.Context, outW, _ io.Writer, _ string, _ ...string) error {
	if outW != nil && len(h.partial) > 0 {
		outW.Write(h.partial)
	}
	<-ctx.Done()
	return ctx.Err()
}

// overrideReapTimeout temporarily shortens reapTimeout so timeout-path
// tests run quickly. Returns a restore function for `defer`.
func overrideReapTimeout(d time.Duration) func() {
	old := reapTimeout
	reapTimeout = d
	return func() { reapTimeout = old }
}

// TestReapClassifiesUnfinishedAsTimedOut exercises issue #68: when
// nix-store stalls past the deadline, paths neither deleted nor
// refused before the timeout fired must land in Summary.TimedOut, not
// Summary.Errors. Reap inspects ctx.Err() to make the call.
func TestReapClassifiesUnfinishedAsTimedOut(t *testing.T) {
	defer overrideReapTimeout(50 * time.Millisecond)()
	defer overrideRunner(hangingRunner{
		partial: []byte("deleting '/nix/store/reclaimed'\n"),
	})()

	plan := Plan{Closure: []string{
		"/nix/store/reclaimed",
		"/nix/store/in-flight-1",
		"/nix/store/in-flight-2",
	}}

	s := Reap(plan, nil, nil)
	if s.Reclaimed != 1 {
		t.Errorf("Reclaimed = %d, want 1", s.Reclaimed)
	}
	if s.TimedOut != 2 {
		t.Errorf("TimedOut = %d, want 2", s.TimedOut)
	}
	if len(s.Errors) != 0 {
		t.Errorf("expected timeout to bypass Errors, got %v", s.Errors)
	}
}

// TestReapNonTimeoutUnaccountedStillSurfacesAsError verifies that the
// non-timeout error-classification path still works after the timeout
// branch was added: when ctx is healthy but paths went unaccounted,
// the existing Errors entry is produced.
func TestReapNonTimeoutUnaccountedStillSurfacesAsError(t *testing.T) {
	defer overrideRunner(reapStub{
		runOutput: []byte("deleting '/nix/store/a'\n"),
		runErr:    errors.New("exit status 1"),
	})()

	plan := Plan{Closure: []string{
		"/nix/store/a",
		"/nix/store/unaccounted",
	}}
	s := Reap(plan, nil, nil)

	if s.TimedOut != 0 {
		t.Errorf("TimedOut should be 0 for non-timeout case, got %d", s.TimedOut)
	}
	if len(s.Errors) != 1 {
		t.Errorf("expected one error entry for unaccounted path, got %v", s.Errors)
	}
}
