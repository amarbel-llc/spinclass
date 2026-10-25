package sysprompt

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writePage writes a manpage into <root>/man<section>/<name>.<section>,
// gzipping it when gz is set, so the scanner is exercised against both the
// compressed and plain forms a real manpath mixes.
func writePage(t *testing.T, root, section, name, body string, gz bool) string {
	t.Helper()
	dir := filepath.Join(root, "man"+section)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+"."+section)
	data := []byte(body)
	if gz {
		path += ".gz"
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		data = buf.Bytes()
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func noDeadline() time.Time { return time.Time{} }

// The three NAME dialects that actually occur in a nix profile all parse. A
// census of this host's 1233 profile pages found 1152 man(7) `.SH NAME` (699
// separated by ` \- `, 448 by a plain ` - `), 36 mdoc(7) `.Nd`, and 45 with no
// NAME section at all — so all four cases below are real, not hypothetical.
func TestRenderManIndexDialects(t *testing.T) {
	root := t.TempDir()
	// man(7), escaped separator, gzipped — the most common shape.
	writePage(t, root, "1", "acyclic", ".SH NAME\nacyclic \\- make directed graph acyclic\n.SH SYNOPSIS\n", true)
	// man(7), quoted heading and roff font escapes around the name.
	writePage(t, root, "1", "age", ".SH \"NAME\"\n\\fBage\\fR \\- simple, modern file encryption\n", true)
	// scdoc: a `.PP` macro sits between the heading and the content line, and
	// the separator is a plain dash.
	writePage(t, root, "7", "eng", ".SH NAME\n.PP\neng - personal development environment monorepo\n.PP\n", true)
	// mdoc(7): the description rides its own macro.
	writePage(t, root, "5", "mdocish", ".Sh NAME\n.Nm mdocish\n.Nd an mdoc formatted page\n", false)

	out := renderManIndex([]string{root}, defaultIndexLimit, noDeadline())

	mustContain(t, out, "## Manpage index")
	mustContain(t, out, "- `acyclic(1)` — make directed graph acyclic")
	mustContain(t, out, "- `age(1)` — simple, modern file encryption")
	mustContain(t, out, "- `eng(7)` — personal development environment monorepo")
	mustContain(t, out, "- `mdocish(5)` — an mdoc formatted page")
}

// roff wraps freely, so a NAME description may span physical lines. Reading
// only the first one rendered hyphence(1) as "…re-emission of on-disk", cut
// mid-thought; lexgrog joins them, and so must this. Verbatim from the real
// page.
func TestRenderManIndexJoinsWrappedNameLines(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "1", "hyphence",
		".SH NAME\nhyphence \\- format\\-only inspection and re\\-emission of on\\-disk\nhyphence documents\n.SH SYNOPSIS\n", true)

	out := renderManIndex([]string{root}, defaultIndexLimit, noDeadline())

	mustContain(t, out, "- `hyphence(1)` — format-only inspection and re-emission of on-disk hyphence documents")
}

// The join must stop at the block boundary rather than swallowing the next
// section: a macro or a blank line after content ends the description.
func TestRenderManIndexJoinStopsAtBlockEnd(t *testing.T) {
	root := t.TempDir()
	// scdoc shape: .PP before the content, another .PP after it.
	writePage(t, root, "7", "scdocish", ".SH NAME\n.PP\nscdocish - a short summary\n.PP\n.SH DESCRIPTION\nnot part of NAME\n", true)
	writePage(t, root, "7", "blankish", ".SH NAME\nblankish \\- ends at the blank\n\nnot part of NAME\n", true)

	out := renderManIndex([]string{root}, defaultIndexLimit, noDeadline())

	mustContain(t, out, "- `scdocish(7)` — a short summary")
	mustContain(t, out, "- `blankish(7)` — ends at the blank")
	if strings.Contains(out, "not part of NAME") {
		t.Errorf("the join must stop at the end of the NAME block:\n%s", out)
	}
}

// A generator can emit an essay into NAME — spinclass's own section-1 pages
// carry whole MCP tool descriptions (~1200 chars) — and one such page would
// otherwise dominate the index.
func TestRenderManIndexTruncatesOverlongName(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "1", "verbose",
		".SH NAME\nverbose \\- "+strings.Repeat("essay ", 100)+"\n", true)

	out := renderManIndex([]string{root}, defaultIndexLimit, noDeadline())

	mustContain(t, out, "…")
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "- `verbose(1)`") && len(line) > maxDescLen+40 {
			t.Errorf("overlong NAME not truncated (%d chars): %q", len(line), line)
		}
	}
}

// A hyphenated word inside a description is written `agent\-backed` and must
// not be mistaken for the ` \- ` name/description separator.
func TestRenderManIndexHyphenInDescription(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "1", "age-plugin-piggy",
		".SH NAME\nage\\-plugin\\-piggy \\- age plugin: PIV/agent\\-backed P\\-256 identities\n", true)

	out := renderManIndex([]string{root}, defaultIndexLimit, noDeadline())

	mustContain(t, out, "- `age-plugin-piggy(1)` — age plugin: PIV/agent-backed P-256 identities")
}

// The page name comes from the filename, not the NAME line: it is what `man`
// takes as an argument, and some pages document a differently-named command
// (awk.1 names gawk).
func TestRenderManIndexNameFromFilename(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "1", "awk", ".SH NAME\ngawk \\- pattern scanning language\n", true)

	out := renderManIndex([]string{root}, defaultIndexLimit, noDeadline())

	mustContain(t, out, "- `awk(1)` — pattern scanning language")
	if strings.Contains(out, "`gawk(1)`") {
		t.Errorf("name must come from the filename, not the NAME line:\n%s", out)
	}
}

// A page with no NAME section still earns a row — it exists and is readable,
// it just has nothing to say about itself.
func TestRenderManIndexNoNameSectionStillLists(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "7", "bare", ".TH bare 7\n.SH DESCRIPTION\nno name section here\n", false)

	out := renderManIndex([]string{root}, defaultIndexLimit, noDeadline())

	mustContain(t, out, "- `bare(7)`")
	if strings.Contains(out, "bare(7)` — ") {
		t.Errorf("a page with no NAME must render without a description:\n%s", out)
	}
}

// Off by default and scan-if-exists: no sources, or sources that resolve to
// nothing, render no section at all rather than an empty heading.
func TestRenderManIndexOffAndScanIfExists(t *testing.T) {
	if out := renderManIndex(nil, defaultIndexLimit, noDeadline()); out != "" {
		t.Errorf("nil sources must render nothing, got:\n%s", out)
	}
	if out := renderManIndex([]string{}, defaultIndexLimit, noDeadline()); out != "" {
		t.Errorf("empty sources must render nothing, got:\n%s", out)
	}
	missing := filepath.Join(t.TempDir(), "nope")
	if out := renderManIndex([]string{missing}, defaultIndexLimit, noDeadline()); out != "" {
		t.Errorf("absent source must contribute nothing, got:\n%s", out)
	}
	if out := renderManIndex([]string{filepath.Join(t.TempDir(), "*.7")}, defaultIndexLimit, noDeadline()); out != "" {
		t.Errorf("glob matching nothing must contribute nothing, got:\n%s", out)
	}
}

// A directory source is scanned as a manpath root (its man*/ subdirs); a glob
// source names page files directly. Both reach the same page, and the dedup in
// expandSources keeps it from being listed twice.
func TestRenderManIndexDirAndGlobSourcesDedup(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "7", "one", ".SH NAME\none \\- the first page\n", true)

	fromDir := renderManIndex([]string{root}, defaultIndexLimit, noDeadline())
	mustContain(t, fromDir, "- `one(7)` — the first page")

	fromGlob := renderManIndex([]string{filepath.Join(root, "man*", "*")}, defaultIndexLimit, noDeadline())
	mustContain(t, fromGlob, "- `one(7)` — the first page")

	both := renderManIndex([]string{root, filepath.Join(root, "man*", "*")}, defaultIndexLimit, noDeadline())
	if n := strings.Count(both, "`one(7)`"); n != 1 {
		t.Errorf("a page reachable from two sources must be listed once, got %d:\n%s", n, both)
	}
}

// Pointing at a section directory rather than the manpath root above it is an
// easy mistake whose failure mode would otherwise be silence. The fallback is
// scoped to man*-named directories, so an unrelated directory holding
// dot-suffixed files is not mistaken for a page source.
func TestRenderManIndexSectionDirFallback(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "7", "sectioned", ".SH NAME\nsectioned \\- found via its section dir\n", true)

	out := renderManIndex([]string{filepath.Join(root, "man7")}, defaultIndexLimit, noDeadline())
	mustContain(t, out, "- `sectioned(7)` — found via its section dir")

	notMan := filepath.Join(root, "notman")
	if err := os.MkdirAll(notMan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notMan, "config.toml"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := renderManIndex([]string{notMan}, defaultIndexLimit, noDeadline()); out != "" {
		t.Errorf("a non-man directory must not be scanned for pages, got:\n%s", out)
	}
}

// The entry cap is the guardrail against a bulk selector (a whole $MANPATH is
// ~1200 pages on this host). Past it the scan stops and says so, rather than
// silently truncating. A limit <= 0 opts out of capping entirely.
func TestRenderManIndexCapsEntries(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 12; i++ {
		writePage(t, root, "1", "page"+strconv.Itoa(i), ".SH NAME\nx \\- d\n", false)
	}

	out := renderManIndex([]string{root}, 5, noDeadline())
	if got := strings.Count(out, "\n- `"); got != 5 {
		t.Errorf("rendered %d rows, want exactly the limit of 5", got)
	}
	mustContain(t, out, "…and 7 more (not indexed; narrow the selector)")

	uncapped := renderManIndex([]string{root}, 0, noDeadline())
	if got := strings.Count(uncapped, "\n- `"); got != 12 {
		t.Errorf("limit 0 must not cap: rendered %d rows, want all 12", got)
	}
	if strings.Contains(uncapped, "not indexed") {
		t.Errorf("an uncapped index must not report a shortfall:\n%s", uncapped)
	}
}

// Truncation must not be biased by section directory. Sorting by file PATH
// grouped man1 entirely before man7, so a corpus past the cap lost whole
// sections — on the real first-party manpath that dropped every eng-*(7)
// convention page, the ones the index exists for, while keeping 200
// per-subcommand man1 pages. Ordering by the rendered name spreads the cut.
func TestRenderManIndexCapIsNotSectionBiased(t *testing.T) {
	root := t.TempDir()
	// Two man1 pages that sort AFTER the man7 page by name, so a path-ordered
	// cap of 2 would keep both man1 pages and drop the man7 one.
	writePage(t, root, "1", "zeta", ".SH NAME\nzeta \\- a man1 page\n", false)
	writePage(t, root, "1", "yankee", ".SH NAME\nyankee \\- another man1 page\n", false)
	writePage(t, root, "7", "alpha", ".SH NAME\nalpha \\- a man7 convention page\n", false)

	out := renderManIndex([]string{root}, 2, noDeadline())

	mustContain(t, out, "- `alpha(7)` — a man7 convention page")
	if strings.Contains(out, "zeta(1)") {
		t.Errorf("cap must follow name order, not section order:\n%s", out)
	}
}

// An already-expired deadline stops the scan rather than walking the whole
// source: the fragment is fetched before the agent's `initialize` and must
// never stall. The shortfall is reported, not hidden.
func TestRenderManIndexHonorsDeadline(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 64; i++ {
		writePage(t, root, "1", "page"+strconv.Itoa(i), ".SH NAME\nx \\- d\n", false)
	}

	out := renderManIndex([]string{root}, defaultIndexLimit, time.Now().Add(-time.Second))

	mustContain(t, out, "## Manpage index")
	mustContain(t, out, "more (not indexed; narrow the selector)")
}

// An unsupported compression is reported rather than silently skipped, so a
// gap in the index is always visible to the reader.
func TestRenderManIndexUnsupportedCompressionWarned(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "man1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zstdpage.1.zst"), []byte("not really zstd"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := renderManIndex([]string{root}, defaultIndexLimit, noDeadline())

	mustContain(t, out, "**⚠ not indexed**")
	mustContain(t, out, "unsupported compression .zst")
}
