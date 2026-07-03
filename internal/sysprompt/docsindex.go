package sysprompt

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// defaultDocIndexDirs is the built-in scan set for the design-record index,
// used when a sweatfile does not set [sysprompt].doc-index-dirs. Scan-if-
// exists: a dir that is absent simply contributes nothing. See FDR 0021.
var defaultDocIndexDirs = []string{"docs/features", "docs/adrs", "docs/rfcs"}

// genreTags maps a known design-record dir to its record label so numbers do
// not collide across genres (FDR 0014 vs ADR 0014). A dir not listed here
// falls back to its path basename as the tag.
var genreTags = map[string]string{
	"docs/features": "FDR",
	"docs/adrs":     "ADR",
	"docs/rfcs":     "RFC",
}

// maxWarnings caps the malformed-file diagnostics rendered into the fragment so
// a wholly-broken dir cannot bloat the system prompt; the remainder collapses
// to a single "…and N more" line.
const maxWarnings = 10

var (
	// recordFileRe matches an NNNN-title.md design-record filename, capturing
	// the zero-padded number and the title slug.
	recordFileRe = regexp.MustCompile(`^(\d{4})-(.+)\.md$`)
	// headingRe matches the first level-1 markdown heading (the record title).
	headingRe = regexp.MustCompile(`(?m)^#[ \t]+(.+?)[ \t]*$`)
	// statusRe matches a `status:` frontmatter line.
	statusRe = regexp.MustCompile(`(?m)^status:[ \t]*(.+?)[ \t]*$`)
)

type designRecord struct {
	genre  string
	number string
	title  string
	status string // "" when the file carries no status frontmatter
}

// renderDesignRecords scans dirs (relative to root) for NNNN-*.md design
// records and returns a "## Design records" markdown section listing them by
// number · title · status, grouped by status. Malformed records (unreadable
// files, or a frontmatter block opened but never closed) are NOT silently
// dropped: they are surfaced as a diagnostic block appended to the section so
// the user and the agent are told which files could not be indexed. It returns
// "" only when root/dirs is empty or nothing at all (records or warnings) was
// found.
//
// Best-effort is a hard guarantee here: the fragment is fetched before the
// agent's `initialize`, so a malformed doc must never take the render down. A
// recover() converts any unexpected panic into a warning line rather than
// letting it escape.
func renderDesignRecords(root string, dirs []string) (section string) {
	if root == "" || len(dirs) == 0 {
		return ""
	}
	var (
		records  []designRecord
		warnings []string
	)
	defer func() {
		if r := recover(); r != nil {
			warnings = append(warnings, fmt.Sprintf("design-record scan aborted: %v", r))
			section = formatRecords(records, warnings)
		}
	}()

	for _, dir := range dirs {
		genre := genreTags[dir]
		if genre == "" {
			genre = filepath.Base(dir)
		}
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			// Absent dir is scan-if-exists (silent); any other read failure
			// (e.g. a permission error) is a diagnostic, not a silent drop.
			if !os.IsNotExist(err) {
				warnings = append(warnings, dir+" — unreadable directory: "+err.Error())
			}
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			m := recordFileRe.FindStringSubmatch(e.Name())
			if m == nil {
				continue // not a design record — silently ignored (README, etc.)
			}
			rel := filepath.Join(dir, e.Name())
			data, err := os.ReadFile(filepath.Join(root, dir, e.Name()))
			if err != nil {
				warnings = append(warnings, rel+" — unreadable: "+err.Error())
				continue
			}
			status, malformed := parseStatus(string(data))
			if malformed != "" {
				warnings = append(warnings, rel+" — "+malformed)
				continue // broken metadata: surface it, don't list it as a record
			}
			title := extractHeading(string(data))
			if title == "" {
				title = slugToTitle(m[2])
			}
			records = append(records, designRecord{
				genre:  genre,
				number: m[1],
				title:  title,
				status: status,
			})
		}
	}
	return formatRecords(records, warnings)
}

// extractHeading returns the first level-1 markdown heading, or "" if none.
func extractHeading(body string) string {
	if m := headingRe.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// parseStatus returns the frontmatter `status:` value and, when the file's
// frontmatter is structurally malformed (a block opened with `---` but never
// closed), a non-empty diagnostic describing the problem. An absent frontmatter
// block and a present-but-status-less block are both legitimate (no diagnostic,
// empty status) — only a broken block is flagged.
func parseStatus(body string) (status, malformed string) {
	content, state := frontmatter(body)
	switch state {
	case fmUnterminated:
		return "", "unterminated frontmatter block"
	case fmNone:
		return "", ""
	}
	if m := statusRe.FindStringSubmatch(content); m != nil {
		return m[1], ""
	}
	return "", ""
}

type fmState int

const (
	fmNone         fmState = iota // no leading `---` block
	fmOK                          // block opened and closed
	fmUnterminated                // block opened but never closed — malformed
)

// frontmatter classifies and returns a leading `---`-delimited YAML block.
// Scoping the status search to this block keeps a stray `status:` line
// elsewhere in the document from matching.
func frontmatter(body string) (string, fmState) {
	body = strings.TrimPrefix(body, string(rune(0xFEFF))) // tolerate a leading UTF-8 BOM
	nl := strings.IndexByte(body, '\n')
	if nl < 0 || strings.TrimRight(body[:nl], "\r") != "---" {
		return "", fmNone
	}
	rest := body[nl+1:]
	if i := strings.Index(rest, "\n---"); i >= 0 {
		return rest[:i], fmOK
	}
	return "", fmUnterminated
}

// slugToTitle turns a filename slug (NNNN-<slug>.md) into a fallback title
// used only when a record carries no level-1 heading.
func slugToTitle(slug string) string {
	return strings.ReplaceAll(slug, "-", " ")
}

// statusRank orders status groups by lifecycle maturity (settled first). An
// unrecognised but present status sorts after the known ones; the unstatused
// bucket sorts last. The switch keys off the first token so a compound status
// like "superseded by FDR-0021" ranks with "superseded".
func statusRank(status string) int {
	switch firstToken(status) {
	case "accepted":
		return 0
	case "testing":
		return 1
	case "experimental":
		return 2
	case "proposed":
		return 3
	case "exploring":
		return 4
	case "deprecated":
		return 5
	case "superseded":
		return 6
	case "relocated":
		return 7
	case "dormant":
		return 8
	case "":
		return 100 // unstatused — last
	default:
		return 50 // present but unrecognised — before unstatused
	}
}

func firstToken(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// formatRecords groups records by status and renders the markdown section,
// appending a malformed-file diagnostic block when warnings were collected.
// Returns "" when there is nothing to show at all.
func formatRecords(records []designRecord, warnings []string) string {
	if len(records) == 0 && len(warnings) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Design records\n")

	groups := map[string][]designRecord{}
	for _, r := range records {
		groups[r.status] = append(groups[r.status], r)
	}
	statuses := make([]string, 0, len(groups))
	for s := range groups {
		statuses = append(statuses, s)
	}
	sort.Slice(statuses, func(i, j int) bool {
		if ri, rj := statusRank(statuses[i]), statusRank(statuses[j]); ri != rj {
			return ri < rj
		}
		return statuses[i] < statuses[j]
	})
	for _, s := range statuses {
		recs := groups[s]
		sort.Slice(recs, func(i, j int) bool {
			if recs[i].genre != recs[j].genre {
				return recs[i].genre < recs[j].genre
			}
			return recs[i].number < recs[j].number
		})
		heading := s
		if heading == "" {
			heading = "(unstatused)"
		}
		b.WriteString("\n**" + heading + "**\n")
		for _, r := range recs {
			b.WriteString("- " + r.genre + " " + r.number + " — " + r.title + "\n")
		}
	}

	if len(warnings) > 0 {
		sort.Strings(warnings)
		b.WriteString("\n**⚠ malformed — not indexed**\n")
		shown, extra := warnings, 0
		if len(shown) > maxWarnings {
			extra = len(shown) - maxWarnings
			shown = shown[:maxWarnings]
		}
		for _, w := range shown {
			b.WriteString("- " + w + "\n")
		}
		if extra > 0 {
			fmt.Fprintf(&b, "- …and %d more\n", extra)
		}
	}

	return strings.TrimRight(b.String(), "\n")
}
