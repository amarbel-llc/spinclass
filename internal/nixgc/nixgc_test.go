package nixgc

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"
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

func (s stubRunner) Run(outW, _ io.Writer, _ string, _ ...string) error {
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

func (s *sequencedRunner) Run(_, _ io.Writer, _ string, _ ...string) error {
	return errors.New("sequencedRunner.Run should not be invoked")
}
