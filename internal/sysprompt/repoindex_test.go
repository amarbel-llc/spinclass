package sysprompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeCheckout creates a directory that looks like a git checkout (a `.git`
// entry is the whole test) with optional flake.nix and README.md content.
func makeCheckout(t *testing.T, parent, name, flake, readme string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if flake != "" {
		if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(flake), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if readme != "" {
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A directory of checkouts is the selector the fleet root uses: every
// immediate child holding a .git is indexed, and non-checkouts are skipped.
func TestRenderRepoIndexScansDirOfCheckouts(t *testing.T) {
	repos := t.TempDir()
	makeCheckout(t, repos, "spinclass", `{ description = "git worktree session manager"; }`, "")
	makeCheckout(t, repos, "clown", `{ description = "an agent harness"; }`, "")
	if err := os.MkdirAll(filepath.Join(repos, "not-a-repo"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := renderRepoIndex([]string{repos}, noDeadline())

	mustContain(t, out, "## Repository index")
	mustContain(t, out, "- `spinclass` — git worktree session manager")
	mustContain(t, out, "- `clown` — an agent harness")
	if strings.Contains(out, "not-a-repo") {
		t.Errorf("a directory without .git is not a checkout:\n%s", out)
	}
}

// A source that is itself a checkout is indexed directly rather than scanned
// for children, so an explicit list of repo paths works too.
func TestRenderRepoIndexDirectCheckoutSource(t *testing.T) {
	parent := t.TempDir()
	repo := makeCheckout(t, parent, "solo", `{ description = "a single named repo"; }`, "")

	out := renderRepoIndex([]string{repo}, noDeadline())

	mustContain(t, out, "- `solo` — a single named repo")
}

// The flake description wins over the README when both exist.
func TestRenderRepoIndexPrefersFlakeDescription(t *testing.T) {
	repos := t.TempDir()
	makeCheckout(t, repos, "both",
		"{\n  description = \"from the flake\";\n}\n",
		"# both\n\nfrom the readme instead\n")

	out := renderRepoIndex([]string{repos}, noDeadline())

	mustContain(t, out, "- `both` — from the flake")
	if strings.Contains(out, "from the readme") {
		t.Errorf("flake description must win over README:\n%s", out)
	}
}

// Without a flake description, the README's first prose line is used —
// skipping headings, badges, links and other structure.
func TestRenderRepoIndexReadmeFallbackSkipsStructure(t *testing.T) {
	repos := t.TempDir()
	makeCheckout(t, repos, "readme-only", "",
		"# readme-only\n\n"+
			"![badge](https://example.invalid/b.svg)\n"+
			"[a link line](https://example.invalid)\n"+
			"<img src=\"x\">\n"+
			"> a quote\n"+
			"- a list item\n\n"+
			"A tool that does the thing it says.\n")

	out := renderRepoIndex([]string{repos}, noDeadline())

	mustContain(t, out, "- `readme-only` — A tool that does the thing it says.")
}

// A checkout with neither source still earns a row: it exists, it just has no
// description. That is not a warning-worthy condition.
func TestRenderRepoIndexUndescribedStillLists(t *testing.T) {
	repos := t.TempDir()
	makeCheckout(t, repos, "quiet", "", "")

	out := renderRepoIndex([]string{repos}, noDeadline())

	mustContain(t, out, "- `quiet`")
	if strings.Contains(out, "`quiet` — ") {
		t.Errorf("a checkout with no description must render without one:\n%s", out)
	}
	if strings.Contains(out, "⚠") {
		t.Errorf("a missing description is not a warning:\n%s", out)
	}
}

// A long README line is truncated on a word boundary so the index cannot be
// bloated by one verbose repo.
func TestRenderRepoIndexTruncatesLongDescription(t *testing.T) {
	repos := t.TempDir()
	long := strings.Repeat("verbose ", 60)
	makeCheckout(t, repos, "wordy", "", "# wordy\n\n"+long+"\n")

	out := renderRepoIndex([]string{repos}, noDeadline())

	mustContain(t, out, "…")
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "- `wordy`") && len(line) > maxDescLen+40 {
			t.Errorf("description not truncated (%d chars): %q", len(line), line)
		}
	}
}

// Escapes in a Nix double-quoted description resolve to their authored text.
func TestRenderRepoIndexUnescapesNixString(t *testing.T) {
	repos := t.TempDir()
	makeCheckout(t, repos, "quoted", `{ description = "a \"quoted\" thing"; }`, "")

	out := renderRepoIndex([]string{repos}, noDeadline())

	mustContain(t, out, `- `+"`quoted`"+` — a "quoted" thing`)
}

// Off by default and scan-if-exists, matching the manpage and design-record
// indexes.
func TestRenderRepoIndexOffAndScanIfExists(t *testing.T) {
	if out := renderRepoIndex(nil, noDeadline()); out != "" {
		t.Errorf("nil sources must render nothing, got:\n%s", out)
	}
	if out := renderRepoIndex([]string{}, noDeadline()); out != "" {
		t.Errorf("empty sources must render nothing, got:\n%s", out)
	}
	missing := filepath.Join(t.TempDir(), "nope")
	if out := renderRepoIndex([]string{missing}, noDeadline()); out != "" {
		t.Errorf("absent source must contribute nothing, got:\n%s", out)
	}
	// A directory with no checkouts under it is not an error, just empty.
	if out := renderRepoIndex([]string{t.TempDir()}, noDeadline()); out != "" {
		t.Errorf("dir with no checkouts must render nothing, got:\n%s", out)
	}
}

// The same cap guards the repo index, since a directory-of-checkouts selector
// is equally unbounded in principle.
func TestRenderRepoIndexCapsEntries(t *testing.T) {
	repos := t.TempDir()
	for i := 0; i < maxIndexEntries+3; i++ {
		makeCheckout(t, repos, "repo"+itoaTest(i), "", "")
	}

	out := renderRepoIndex([]string{repos}, noDeadline())

	if got := strings.Count(out, "\n- `"); got > maxIndexEntries {
		t.Errorf("rendered %d rows, want at most %d", got, maxIndexEntries)
	}
	mustContain(t, out, "more (not indexed; narrow the selector)")
}
