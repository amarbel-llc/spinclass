package sysprompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxIndexEntries caps the rows any one sweatfile-selected index renders. The
// selectors accept bulk sources ($MANPATH, a directory of checkouts), and a
// full profile manpath is ~1200 pages on a developer host — far past what
// belongs in every session's system prompt. Past the cap the scan stops and
// the section reports how many more matched. See FDR 0030.
const maxIndexEntries = 200

// maxNameBytes bounds how much of a page is read looking for its NAME section.
// The section is a header block by construction, so this is generous; it keeps
// a bulk scan from decompressing whole manuals.
const maxNameBytes = 8 << 10

// globMeta are the characters that make a source spec a glob rather than a
// literal path.
const globMeta = `*?[`

// expandSources turns sweatfile source specs into concrete filesystem paths.
//
// Each spec is expanded for `~` and `$VAR`, then split on ":" so a bare
// "$MANPATH" (itself a colon-joined list, often with empty leading entries)
// expands to its individual roots. A spec containing glob metacharacters is
// matched with filepath.Glob; anything else is passed through literally for
// the caller to classify. Order is preserved and duplicates are dropped, so a
// path reachable from two specs is indexed once.
//
// A glob that matches nothing contributes nothing — bulk selectors are
// scan-if-exists, matching the design-record index (FDR 0021).
func expandSources(specs []string) []string {
	var (
		out  []string
		seen = map[string]bool{}
	)
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, spec := range specs {
		for _, entry := range strings.Split(expandPath(spec), ":") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			if strings.ContainsAny(entry, globMeta) {
				matches, err := filepath.Glob(entry)
				if err != nil {
					continue // a malformed pattern contributes nothing
				}
				for _, m := range matches {
					add(m)
				}
				continue
			}
			add(entry)
		}
	}
	return out
}

// expandPath resolves a leading `~` against the home directory and expands
// $VAR references. An unset variable expands to empty, which the caller drops.
func expandPath(p string) string {
	p = os.ExpandEnv(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// indexEntry is one rendered row: a name and the short description scraped
// from the source.
type indexEntry struct {
	name string
	desc string
}

// formatIndex renders a section heading, a one-line instruction, and the entry
// rows, appending the capped-warning block shared with the design-record index.
// It returns "" when there is nothing at all to show, so an index whose sources
// match nothing is invisible rather than an empty heading.
func formatIndex(heading, instruction string, entries []indexEntry, warnings []string, truncated int) string {
	// truncated counts alone still render: a scan that hit the entry cap or ran
	// out of deadline before indexing anything must say so, not vanish.
	if len(entries) == 0 && len(warnings) == 0 && truncated == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## " + heading + "\n")
	if instruction != "" {
		b.WriteString("\n" + instruction + "\n")
	}
	if len(entries) > 0 || truncated > 0 {
		b.WriteString("\n")
		for _, e := range entries {
			if e.desc == "" {
				b.WriteString("- `" + e.name + "`\n")
				continue
			}
			b.WriteString("- `" + e.name + "` — " + e.desc + "\n")
		}
	}
	if truncated > 0 {
		fmt.Fprintf(&b, "- …and %d more (not indexed; narrow the selector)\n", truncated)
	}
	if len(warnings) > 0 {
		b.WriteString("\n**⚠ not indexed**\n")
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
