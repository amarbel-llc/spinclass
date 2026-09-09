package sysprompt

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// manIndexInstruction is the single line of guidance rendered under the
// manpage-index heading. It points at the man MCP tools rather than restating
// what each page contains — the table below it already does that.
const manIndexInstruction = "These are authoritative for the conventions they describe — consult the matching page before guessing. Read them with the `man.*` MCP tools (`man_toc`, then `man_section`)."

var (
	// manNameHeadingRe matches a NAME section heading in either roff dialect:
	// man(7) `.SH NAME` / `.SH "NAME"` and mdoc(7) `.Sh NAME`.
	manNameHeadingRe = regexp.MustCompile(`(?mi)^\.S[Hh][ \t]+"?NAME"?[ \t]*$`)
	// manNdRe matches an mdoc(7) `.Nd <description>` line, which carries the
	// short description directly rather than in a `name - desc` pair.
	manNdRe = regexp.MustCompile(`(?m)^\.Nd[ \t]+(.+?)[ \t]*$`)
	// roffFontRe matches the roff font escapes that pepper NAME lines in
	// pages generated from DocBook/asciidoc (`\fBage\fR \- …`).
	roffFontRe = regexp.MustCompile(`\\f(\([A-Za-z]{2}|[A-Za-z0-9])`)
)

// renderManIndex scans the pages selected by sources and returns a
// "## Manpage index" markdown section listing each as `name(section)` plus the
// short description scraped from its NAME block. Returns "" when sources is
// empty or nothing was found.
//
// Best-effort is a hard guarantee, exactly as for the design-record index: the
// fragment is fetched before the agent's `initialize`, so an unreadable or
// malformed page must never take the render down. A recover() converts any
// unexpected panic into a warning line.
func renderManIndex(sources []string, deadline time.Time) (section string) {
	if len(sources) == 0 {
		return ""
	}
	var (
		entries   []indexEntry
		warnings  []string
		truncated int
	)
	defer func() {
		if r := recover(); r != nil {
			warnings = append(warnings, fmt.Sprintf("manpage scan aborted: %v", r))
			section = formatIndex("Manpage index", manIndexInstruction, entries, warnings, truncated)
		}
	}()

	files, warnings := collectManFiles(sources, warnings)
	sort.Strings(files)
	if len(files) > maxIndexEntries {
		truncated = len(files) - maxIndexEntries
		files = files[:maxIndexEntries]
	}

	for i, f := range files {
		// The scan is local I/O but unbounded in principle (a bulk selector
		// can name a whole profile manpath), so it yields to the deadline
		// rather than risk stalling the pre-initialize prompts/get.
		if i%32 == 0 && !deadline.IsZero() && time.Now().After(deadline) {
			truncated += len(files) - i
			break
		}
		name, ok := manPageName(f)
		if !ok {
			continue // not a page file (a stray README in a man dir)
		}
		desc, err := manPageDescription(f)
		if err != nil {
			warnings = append(warnings, name+" — "+err.Error())
			continue
		}
		entries = append(entries, indexEntry{name: name, desc: desc})
	}
	return formatIndex("Manpage index", manIndexInstruction, entries, warnings, truncated)
}

// collectManFiles resolves source paths into page files. A directory is
// treated as a manpath root and scanned one level into its `man*` section
// dirs; anything else is taken as a page path. A source that does not exist
// contributes nothing (scan-if-exists); any other stat failure is a warning.
func collectManFiles(sources []string, warnings []string) ([]string, []string) {
	var (
		files []string
		seen  = map[string]bool{}
	)
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	for _, src := range expandSources(sources) {
		info, err := os.Stat(src)
		if err != nil {
			if !os.IsNotExist(err) {
				warnings = append(warnings, src+" — unreadable: "+err.Error())
			}
			continue
		}
		if !info.IsDir() {
			add(src)
			continue
		}
		sectionDirs, err := filepath.Glob(filepath.Join(src, "man*"))
		if err != nil {
			continue
		}
		// Pointing at a section directory (…/share/man/man7) instead of the
		// manpath root above it is an easy mistake, and it would otherwise
		// contribute nothing — indistinguishable from "not configured". Only
		// a man*-named directory gets this fallback, so an unrelated
		// directory is not turned into a page source.
		if len(sectionDirs) == 0 && strings.HasPrefix(filepath.Base(src), "man") {
			sectionDirs = []string{src}
		}
		for _, sd := range sectionDirs {
			pages, err := os.ReadDir(sd)
			if err != nil {
				warnings = append(warnings, sd+" — unreadable directory: "+err.Error())
				continue
			}
			for _, p := range pages {
				if !p.IsDir() {
					add(filepath.Join(sd, p.Name()))
				}
			}
		}
	}
	return files, warnings
}

// manPageName derives the `name(section)` label from a page's filename —
// `eng-direnv.7.gz` becomes `eng-direnv(7)`. The filename, not the NAME line,
// is authoritative: it is what `man` takes as an argument, and some pages
// document a different command in NAME than they are filed under (awk.1
// names gawk). Reports false when the filename has no section suffix.
func manPageName(path string) (string, bool) {
	base := filepath.Base(path)
	for _, ext := range []string{".gz", ".bz2", ".xz", ".zst", ".Z", ".lzma"} {
		base = strings.TrimSuffix(base, ext)
	}
	dot := strings.LastIndex(base, ".")
	if dot <= 0 || dot == len(base)-1 {
		return "", false
	}
	return base[:dot] + "(" + base[dot+1:] + ")", true
}

// manPageDescription reads the head of a page and extracts the short
// description from its NAME section. Returns an empty description (not an
// error) when the page simply has no NAME section — such a page still earns a
// row, just an undescribed one.
func manPageDescription(path string) (string, error) {
	body, err := readPageHead(path)
	if err != nil {
		return "", err
	}
	// mdoc(7) carries the description on its own `.Nd` macro.
	if m := manNdRe.FindStringSubmatch(body); m != nil {
		return cleanRoff(m[1]), nil
	}
	loc := manNameHeadingRe.FindStringIndex(body)
	if loc == nil {
		return "", nil
	}
	return descriptionFromNameBlock(body[loc[1]:]), nil
}

// descriptionFromNameBlock pulls `name - description` out of the lines
// following a NAME heading, skipping the roff control lines (`.PP`, `.Pp`)
// that scdoc-generated pages interpose. It stops at the next section heading.
func descriptionFromNameBlock(rest string) string {
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ".") {
			// Another section began before any content line: no description.
			if strings.HasPrefix(line, ".SH") || strings.HasPrefix(line, ".Sh") {
				return ""
			}
			continue // a formatting macro such as .PP — keep looking
		}
		return splitNameDescription(line)
	}
	return ""
}

// splitNameDescription splits a `name \- description` NAME line and returns
// the cleaned description. Both the escaped (`\-`) and literal (`-`) dash
// separators occur in practice, so both are accepted — but only when
// surrounded by spaces, since hyphenated words inside a description are
// themselves written `agent\-backed` and must not be mistaken for the
// separator. Font escapes are stripped first so `\fBage\fR \- …` splits.
func splitNameDescription(line string) string {
	line = roffFontRe.ReplaceAllString(line, "")
	for _, sep := range []string{` \- `, ` - `, ` \-- `, ` -- `} {
		if i := strings.Index(line, sep); i >= 0 {
			return cleanRoff(line[i+len(sep):])
		}
	}
	return ""
}

// cleanRoff removes the roff escapes that survive into a scraped description
// and collapses whitespace, yielding plain text safe to drop into markdown.
func cleanRoff(s string) string {
	s = roffFontRe.ReplaceAllString(s, "")
	r := strings.NewReplacer(
		`\-`, "-",
		`\ `, " ",
		`\&`, "",
		`\|`, "",
		`\/`, "",
		`\,`, "",
		`\e`, `\`,
		`\(em`, "—",
		`\(en`, "–",
		`\*(lq`, `"`,
		`\*(rq`, `"`,
	)
	s = r.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// readPageHead returns the first maxNameBytes of a page, transparently
// decompressing gzip. Plain and gzipped pages cover every page in a nix
// profile; another compression is reported rather than silently skipped, so a
// gap in the index is always visible.
func readPageHead(path string) (string, error) {
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".gz":
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		zr, err := gzip.NewReader(f)
		if err != nil {
			return "", err
		}
		defer zr.Close()
		// A truncated read of a valid stream returns ErrUnexpectedEOF, which
		// is expected here and not a failure: we only ever want the head.
		b, err := io.ReadAll(io.LimitReader(zr, maxNameBytes))
		if err != nil && len(b) == 0 {
			return "", err
		}
		return string(b), nil
	case ".bz2", ".xz", ".zst", ".lzma", ".z":
		return "", fmt.Errorf("unsupported compression %s", ext)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxNameBytes))
	if err != nil && len(b) == 0 {
		return "", err
	}
	return string(b), nil
}
