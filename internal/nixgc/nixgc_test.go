package nixgc

import (
	"errors"
	"reflect"
	"sort"
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

	got := parseRoots(out, wt)
	want := []Root{
		{LinkPath: "/nix/var/nix/gcroots/auto/abc", StorePath: "/nix/store/aaa-foo"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseRoots() = %v, want %v", got, want)
	}
}

func TestParseRootsLinkInsideWorktree(t *testing.T) {
	// Direct gc root where the link itself lives in the worktree (e.g.
	// `nix-store --add-root <wt>/.gcroot/foo`).
	wt := "/home/u/wt"
	out := "/home/u/wt/.gcroot/foo -> /nix/store/aaa-foo\n"
	defer overrideReadLink(nil)()
	got := parseRoots(out, wt)
	if len(got) != 1 || got[0].LinkPath != "/home/u/wt/.gcroot/foo" {
		t.Errorf("expected in-worktree link to match without readlink, got %v", got)
	}
}

func TestParseRootsDanglingSymlink(t *testing.T) {
	wt := "/home/u/eng/wt/snug-aspen"
	out := "/nix/var/nix/gcroots/auto/abc -> /nix/store/aaa-foo\n"
	// Empty resolver → readLink returns ENOENT-ish error → not in worktree.
	defer overrideReadLink(nil)()
	if got := parseRoots(out, wt); len(got) != 0 {
		t.Errorf("expected dangling-link root to be skipped, got %v", got)
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
	if got := parseRoots(out, wt); len(got) != 0 {
		t.Errorf("expected all malformed lines to be skipped, got %v", got)
	}
}

func TestParseRootsExactWorktreeMatch(t *testing.T) {
	wt := "/home/u/wt"
	out := "/nix/var/nix/gcroots/auto/abc -> /nix/store/aaa\n"
	defer overrideReadLink(map[string]string{
		"/nix/var/nix/gcroots/auto/abc": "/home/u/wt",
	})()
	got := parseRoots(out, wt)
	if len(got) != 1 {
		t.Fatalf("expected 1 root for exact-match worktree path, got %v", got)
	}
}

func TestParseRootsRelativeSymlinkTarget(t *testing.T) {
	wt := "/home/u/wt"
	out := "/home/u/wt/.gcroot/x -> /nix/store/aaa\n"
	// Link is already in worktree → matches without readlink. Verifies the
	// in-worktree fast path doesn't depend on the resolver.
	defer overrideReadLink(map[string]string{})()
	if got := parseRoots(out, wt); len(got) != 1 {
		t.Errorf("expected in-worktree link to match, got %v", got)
	}
}

func TestIsStillAliveRefusal(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"error: cannot delete path '/nix/store/abc' since it is still alive", true},
		{"path is still in use", true},
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
	defer overrideRunner(scriptedRunner{
		results: map[string]runResult{
			"/nix/store/ok":     {output: []byte("deleted\n"), err: nil},
			"/nix/store/alive":  {output: []byte("error: cannot delete path '/nix/store/alive' since it is still alive\n"), err: errors.New("exit status 1")},
			"/nix/store/broken": {output: []byte("error: connection refused\n"), err: errors.New("exit status 1")},
		},
	})()

	s := Reap(plan)
	if s.Reclaimed != 1 {
		t.Errorf("Reclaimed = %d, want 1", s.Reclaimed)
	}
	if s.Kept != 1 {
		t.Errorf("Kept = %d, want 1", s.Kept)
	}
	if len(s.Errors) != 1 {
		t.Errorf("Errors = %v, want 1 entry", s.Errors)
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

type runResult struct {
	output []byte
	err    error
}

type scriptedRunner struct {
	results map[string]runResult
}

func (s scriptedRunner) Output(_ string, _ ...string) ([]byte, error) {
	return nil, nil
}

func (s scriptedRunner) CombinedOutput(_ string, args ...string) ([]byte, error) {
	if len(args) < 2 {
		return nil, errors.New("scripted: need at least 2 args (--delete <path>)")
	}
	path := args[len(args)-1]
	r, ok := s.results[path]
	if !ok {
		return nil, errors.New("scripted: no result for " + path)
	}
	return r.output, r.err
}
