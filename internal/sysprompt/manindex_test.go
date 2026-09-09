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

	out := renderManIndex([]string{root}, noDeadline())

	mustContain(t, out, "## Manpage index")
	mustContain(t, out, "- `acyclic(1)` — make directed graph acyclic")
	mustContain(t, out, "- `age(1)` — simple, modern file encryption")
	mustContain(t, out, "- `eng(7)` — personal development environment monorepo")
	mustContain(t, out, "- `mdocish(5)` — an mdoc formatted page")
}

// A hyphenated word inside a description is written `agent\-backed` and must
// not be mistaken for the ` \- ` name/description separator.
func TestRenderManIndexHyphenInDescription(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "1", "age-plugin-piggy",
		".SH NAME\nage\\-plugin\\-piggy \\- age plugin: PIV/agent\\-backed P\\-256 identities\n", true)

	out := renderManIndex([]string{root}, noDeadline())

	mustContain(t, out, "- `age-plugin-piggy(1)` — age plugin: PIV/agent-backed P-256 identities")
}

// The page name comes from the filename, not the NAME line: it is what `man`
// takes as an argument, and some pages document a differently-named command
// (awk.1 names gawk).
func TestRenderManIndexNameFromFilename(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "1", "awk", ".SH NAME\ngawk \\- pattern scanning language\n", true)

	out := renderManIndex([]string{root}, noDeadline())

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

	out := renderManIndex([]string{root}, noDeadline())

	mustContain(t, out, "- `bare(7)`")
	if strings.Contains(out, "bare(7)` — ") {
		t.Errorf("a page with no NAME must render without a description:\n%s", out)
	}
}

// Off by default and scan-if-exists: no sources, or sources that resolve to
// nothing, render no section at all rather than an empty heading.
func TestRenderManIndexOffAndScanIfExists(t *testing.T) {
	if out := renderManIndex(nil, noDeadline()); out != "" {
		t.Errorf("nil sources must render nothing, got:\n%s", out)
	}
	if out := renderManIndex([]string{}, noDeadline()); out != "" {
		t.Errorf("empty sources must render nothing, got:\n%s", out)
	}
	missing := filepath.Join(t.TempDir(), "nope")
	if out := renderManIndex([]string{missing}, noDeadline()); out != "" {
		t.Errorf("absent source must contribute nothing, got:\n%s", out)
	}
	if out := renderManIndex([]string{filepath.Join(t.TempDir(), "*.7")}, noDeadline()); out != "" {
		t.Errorf("glob matching nothing must contribute nothing, got:\n%s", out)
	}
}

// A directory source is scanned as a manpath root (its man*/ subdirs); a glob
// source names page files directly. Both reach the same page, and the dedup in
// expandSources keeps it from being listed twice.
func TestRenderManIndexDirAndGlobSourcesDedup(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "7", "one", ".SH NAME\none \\- the first page\n", true)

	fromDir := renderManIndex([]string{root}, noDeadline())
	mustContain(t, fromDir, "- `one(7)` — the first page")

	fromGlob := renderManIndex([]string{filepath.Join(root, "man*", "*")}, noDeadline())
	mustContain(t, fromGlob, "- `one(7)` — the first page")

	both := renderManIndex([]string{root, filepath.Join(root, "man*", "*")}, noDeadline())
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

	out := renderManIndex([]string{filepath.Join(root, "man7")}, noDeadline())
	mustContain(t, out, "- `sectioned(7)` — found via its section dir")

	notMan := filepath.Join(root, "notman")
	if err := os.MkdirAll(notMan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notMan, "config.toml"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := renderManIndex([]string{notMan}, noDeadline()); out != "" {
		t.Errorf("a non-man directory must not be scanned for pages, got:\n%s", out)
	}
}

// The entry cap is the guardrail against a bulk selector (a whole $MANPATH is
// ~1200 pages on this host). Past it the scan stops and says so, rather than
// silently truncating.
func TestRenderManIndexCapsEntries(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxIndexEntries+5; i++ {
		writePage(t, root, "1", "page"+strconv.Itoa(i), ".SH NAME\nx \\- d\n", false)
	}

	out := renderManIndex([]string{root}, noDeadline())

	if got := strings.Count(out, "\n- `"); got > maxIndexEntries {
		t.Errorf("rendered %d rows, want at most %d", got, maxIndexEntries)
	}
	mustContain(t, out, "more (not indexed; narrow the selector)")
}

// An already-expired deadline stops the scan rather than walking the whole
// source: the fragment is fetched before the agent's `initialize` and must
// never stall. The shortfall is reported, not hidden.
func TestRenderManIndexHonorsDeadline(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 64; i++ {
		writePage(t, root, "1", "page"+strconv.Itoa(i), ".SH NAME\nx \\- d\n", false)
	}

	out := renderManIndex([]string{root}, time.Now().Add(-time.Second))

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

	out := renderManIndex([]string{root}, noDeadline())

	mustContain(t, out, "**⚠ not indexed**")
	mustContain(t, out, "unsupported compression .zst")
}
