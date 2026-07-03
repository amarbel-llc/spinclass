package sysprompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRaw writes an arbitrary file, for constructing malformed records that
// writeRecord (which always emits well-formed frontmatter) cannot.
func writeRaw(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeRecord creates an NNNN-*.md design record with optional frontmatter
// status, optional level-1 heading, and optional trailing body.
func writeRecord(t *testing.T, dir, name, status, title, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if status != "" {
		b.WriteString("---\nstatus: " + status + "\ndate: 2026-07-02\n---\n\n")
	}
	if title != "" {
		b.WriteString("# " + title + "\n\n")
	}
	b.WriteString(body)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRenderDesignRecordsGroupingAndGenres(t *testing.T) {
	root := t.TempDir()
	features := filepath.Join(root, "docs", "features")
	adrs := filepath.Join(root, "docs", "adrs")
	writeRecord(t, features, "0014-implicit-sessions.md", "accepted", "Implicit sessions", "")
	writeRecord(t, features, "0021-composable.md", "proposed", "Composable dynamic system-prompt", "")
	writeRecord(t, adrs, "0003-foo.md", "accepted", "Foo decision", "")

	out := renderDesignRecords(root, []string{"docs/features", "docs/adrs", "docs/rfcs"})

	mustContain(t, out, "## Design records")
	// Groups ordered by maturity: accepted before proposed.
	if strings.Index(out, "**accepted**") > strings.Index(out, "**proposed**") {
		t.Errorf("accepted should sort before proposed:\n%s", out)
	}
	// Within accepted, genre-sorted (ADR before FDR), and the absent rfcs dir
	// contributes nothing (scan-if-exists).
	mustContain(t, out, "- ADR 0003 — Foo decision")
	mustContain(t, out, "- FDR 0014 — Implicit sessions")
	mustContain(t, out, "- FDR 0021 — Composable dynamic system-prompt")
	if strings.Index(out, "- ADR 0003") > strings.Index(out, "- FDR 0014") {
		t.Errorf("ADR should sort before FDR within a status group:\n%s", out)
	}
	if strings.Contains(out, "RFC") {
		t.Errorf("absent rfcs dir must contribute nothing:\n%s", out)
	}
}

func TestRenderDesignRecordsScanIfExists(t *testing.T) {
	root := t.TempDir() // no docs/ dirs at all
	if out := renderDesignRecords(root, defaultDocIndexDirs); out != "" {
		t.Errorf("no design-record dirs should render nothing, got:\n%s", out)
	}
}

func TestRenderDesignRecordsDisabledByEmptyDirs(t *testing.T) {
	root := t.TempDir()
	writeRecord(t, filepath.Join(root, "docs", "features"), "0001-x.md", "accepted", "X", "")
	if out := renderDesignRecords(root, []string{}); out != "" {
		t.Errorf("empty dir list disables the index, got:\n%s", out)
	}
	if out := renderDesignRecords(root, nil); out != "" {
		t.Errorf("nil dir list renders nothing, got:\n%s", out)
	}
	if out := renderDesignRecords("", defaultDocIndexDirs); out != "" {
		t.Errorf("empty root renders nothing, got:\n%s", out)
	}
}

func TestRenderDesignRecordsUnstatusedAndTitleFallback(t *testing.T) {
	root := t.TempDir()
	features := filepath.Join(root, "docs", "features")
	// No frontmatter and no heading: unstatused, title derived from the slug.
	writeRecord(t, features, "0005-no-status-here.md", "", "", "body only\n")
	// Status present but no heading: title still falls back to the slug.
	writeRecord(t, features, "0006-headless.md", "accepted", "", "body\n")

	out := renderDesignRecords(root, []string{"docs/features"})

	mustContain(t, out, "**(unstatused)**")
	mustContain(t, out, "- FDR 0005 — no status here")
	mustContain(t, out, "- FDR 0006 — headless")
	// Unstatused sorts last.
	if strings.Index(out, "**accepted**") > strings.Index(out, "**(unstatused)**") {
		t.Errorf("unstatused bucket should sort last:\n%s", out)
	}
}

func TestRenderDesignRecordsMalformedSkipped(t *testing.T) {
	root := t.TempDir()
	features := filepath.Join(root, "docs", "features")
	if err := os.MkdirAll(features, 0o755); err != nil {
		t.Fatal(err)
	}
	// Non-record files and a mis-numbered file are all skipped.
	for _, name := range []string{"README.md", "notes.txt", "999-three-digits.md", "index.md"} {
		if err := os.WriteFile(filepath.Join(features, name), []byte("# Nope\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A directory that looks like a record is skipped too.
	if err := os.MkdirAll(filepath.Join(features, "0009-a-dir.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := renderDesignRecords(root, []string{"docs/features"}); out != "" {
		t.Errorf("no valid records should render nothing, got:\n%s", out)
	}
}

func TestRenderDesignRecordsCustomDirGenre(t *testing.T) {
	root := t.TempDir()
	writeRecord(t, filepath.Join(root, "design"), "0001-thing.md", "accepted", "A thing", "")
	out := renderDesignRecords(root, []string{"design"})
	mustContain(t, out, "- design 0001 — A thing")
}

func TestExtractStatusScopedToFrontmatterAndCompound(t *testing.T) {
	root := t.TempDir()
	features := filepath.Join(root, "docs", "features")
	// Compound status; plus a decoy `status:` line in the body that must be
	// ignored because it is outside the frontmatter block.
	writeRecord(t, features, "0002-superseded.md", "superseded by FDR-0021", "Old thing",
		"Some prose.\nstatus: bogus\nmore prose.\n")

	out := renderDesignRecords(root, []string{"docs/features"})

	mustContain(t, out, "**superseded by FDR-0021**")
	mustContain(t, out, "- FDR 0002 — Old thing")
	if strings.Contains(out, "bogus") {
		t.Errorf("body status line must not be picked up:\n%s", out)
	}
}

// A record with a frontmatter block opened but never closed is surfaced as a
// diagnostic, not silently dropped and not listed as a normal (unstatused)
// entry. Valid records alongside it still render.
func TestRenderDesignRecordsMalformedFrontmatterWarned(t *testing.T) {
	root := t.TempDir()
	features := filepath.Join(root, "docs", "features")
	writeRecord(t, features, "0001-good.md", "accepted", "Good one", "")
	writeRaw(t, features, "0099-broken.md", "---\nstatus: proposed\ndate: x\n\n# Broken\nbody\n")

	out := renderDesignRecords(root, []string{"docs/features"})

	mustContain(t, out, "- FDR 0001 — Good one")
	mustContain(t, out, "**⚠ malformed — not indexed**")
	mustContain(t, out, "docs/features/0099-broken.md — unterminated frontmatter block")
	if strings.Contains(out, "- FDR 0099") {
		t.Errorf("malformed record must not be listed as a normal entry:\n%s", out)
	}
}

// A directory of nothing but malformed records still renders the section — the
// warnings are the whole point (notify the user + agent).
func TestRenderDesignRecordsWarningsOnlyStillRenders(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, filepath.Join(root, "docs", "features"), "0001-broken.md", "---\nstatus: x\nno close\n")

	out := renderDesignRecords(root, []string{"docs/features"})
	mustContain(t, out, "## Design records")
	mustContain(t, out, "**⚠ malformed — not indexed**")
	mustContain(t, out, "docs/features/0001-broken.md — unterminated frontmatter block")
}

// A frontmatter block that is closed but carries no status: is legitimate
// (unstatused) — NOT a malformed-file warning.
func TestRenderDesignRecordsStatuslessFrontmatterNotWarned(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, filepath.Join(root, "docs", "features"), "0001-nostatus.md",
		"---\ndate: 2026-07-02\n---\n\n# No status field\n")

	out := renderDesignRecords(root, []string{"docs/features"})
	mustContain(t, out, "**(unstatused)**")
	mustContain(t, out, "- FDR 0001 — No status field")
	if strings.Contains(out, "malformed") {
		t.Errorf("terminated-but-statusless frontmatter must not warn:\n%s", out)
	}
}

// The malformed-file diagnostics are capped so a wholly broken dir cannot
// bloat the fragment.
func TestRenderDesignRecordsWarningsCapped(t *testing.T) {
	root := t.TempDir()
	features := filepath.Join(root, "docs", "features")
	for i := 0; i < maxWarnings+3; i++ {
		writeRaw(t, features, fmt.Sprintf("%04d-broken.md", i+1), "---\nunterminated\n")
	}

	out := renderDesignRecords(root, []string{"docs/features"})
	mustContain(t, out, "…and 3 more")
	if got := strings.Count(out, "unterminated frontmatter block"); got != maxWarnings {
		t.Errorf("listed %d warnings, want cap %d", got, maxWarnings)
	}
}

// A record whose bytes begin with a UTF-8 BOM is still grouped by its real
// frontmatter status, not mis-bucketed as (unstatused). Regression for #204.
func TestRenderDesignRecordsToleratesUTF8BOM(t *testing.T) {
	root := t.TempDir()
	bom := string(rune(0xFEFF))
	writeRaw(t, filepath.Join(root, "docs", "features"), "0001-bommed.md",
		bom+"---\nstatus: accepted\n---\n\n# BOMmed record\n")

	out := renderDesignRecords(root, []string{"docs/features"})

	mustContain(t, out, "**accepted**")
	mustContain(t, out, "- FDR 0001 — BOMmed record")
	if strings.Contains(out, "(unstatused)") {
		t.Errorf("BOM-prefixed record must not be mis-bucketed as unstatused:\n%s", out)
	}
}

func TestParseStatusUnit(t *testing.T) {
	cases := []struct{ body, want string }{
		{"---\nstatus: proposed\n---\n# T\n", "proposed"},
		{"---\nstatus:   experimental  \ndate: x\n---\n", "experimental"},
		{"---\nstatus: superseded by FDR-0021\n---\n", "superseded by FDR-0021"},
		{"# No frontmatter\nstatus: ignored\n", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got, _ := parseStatus(c.body); got != c.want {
			t.Errorf("parseStatus(%q) status = %q, want %q", c.body, got, c.want)
		}
	}
}
