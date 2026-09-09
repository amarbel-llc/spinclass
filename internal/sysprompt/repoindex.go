package sysprompt

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// repoIndexInstruction is the single line of guidance rendered under the
// repository-index heading. Its job is to state the ownership rule the index
// exists to support: work on a repo belongs in that repo's own session.
const repoIndexInstruction = "Sibling repositories checked out on this host. Research and changes belong in the owning repo's own session — hand a question off rather than re-deriving it here."

// maxDescLen bounds a scraped description. flake.nix descriptions are already
// one-liners, but a README first line can run long, and this index renders in
// every session's system prompt.
const maxDescLen = 120

// readmeHeadBytes bounds how much of a README is read for its first prose line.
const readmeHeadBytes = 4 << 10

var (
	// flakeDescRe matches the `description = "…";` attribute every fleet
	// flake carries. Scraping the line is deliberate: `nix flake metadata`
	// would be authoritative but costs an evaluation per repo, far past the
	// pre-initialize budget. The leading boundary allows a single-line flake
	// (`{ description = "…"; }`) while still refusing to match a longer
	// attribute name that merely ends in "description".
	flakeDescRe = regexp.MustCompile(`(?m)(?:^|[\s{])description\s*=\s*"((?:[^"\\]|\\.)*)"`)
	// mdLinkRe unwraps `[text](url)` to `text` so a README line that opens
	// with a link renders as prose.
	mdLinkRe = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// renderRepoIndex scans the checkouts selected by sources and returns a
// "## Repository index" markdown section listing each by name plus a short
// description. Returns "" when sources is empty or nothing was found.
//
// Best-effort with the same hard guarantee as the other indexes: a recover()
// keeps a malformed flake or README from taking down the pre-initialize render.
func renderRepoIndex(sources []string, deadline time.Time) (section string) {
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
			warnings = append(warnings, fmt.Sprintf("repository scan aborted: %v", r))
			section = formatIndex("Repository index", repoIndexInstruction, entries, warnings, truncated)
		}
	}()

	repos, warnings := collectRepos(sources, warnings)
	sort.Strings(repos)
	if len(repos) > maxIndexEntries {
		truncated = len(repos) - maxIndexEntries
		repos = repos[:maxIndexEntries]
	}

	for i, path := range repos {
		if i%16 == 0 && !deadline.IsZero() && time.Now().After(deadline) {
			truncated += len(repos) - i
			break
		}
		entries = append(entries, indexEntry{
			name: filepath.Base(path),
			desc: repoDescription(path),
		})
	}
	return formatIndex("Repository index", repoIndexInstruction, entries, warnings, truncated)
}

// collectRepos resolves source paths into checkout directories. A source that
// is itself a checkout is taken directly; any other directory is scanned one
// level for checkouts, which is what makes `~/eng/repos` a valid selector. A
// source that does not exist contributes nothing.
func collectRepos(sources []string, warnings []string) ([]string, []string) {
	var (
		repos []string
		seen  = map[string]bool{}
	)
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			repos = append(repos, p)
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
			continue // a file is not a checkout
		}
		if isCheckout(src) {
			add(src)
			continue
		}
		children, err := os.ReadDir(src)
		if err != nil {
			warnings = append(warnings, src+" — unreadable directory: "+err.Error())
			continue
		}
		for _, c := range children {
			if !c.IsDir() {
				continue
			}
			if child := filepath.Join(src, c.Name()); isCheckout(child) {
				add(child)
			}
		}
	}
	return repos, warnings
}

// isCheckout reports whether dir holds a git checkout. `.git` is a directory
// in a normal clone and a file in a worktree, so its mere presence is the test.
func isCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// repoDescription resolves a checkout's one-line description, preferring the
// flake's own `description` attribute and falling back to the README's first
// prose line. Returns "" when neither yields anything, which renders as an
// undescribed row rather than a warning — plenty of checkouts legitimately
// have no description.
//
// The forge's repo description is deliberately NOT consulted here: it is a
// network round-trip per repo, and a directory-of-checkouts selector resolves
// dozens, which cannot fit the pre-initialize deadline. See FDR 0030.
func repoDescription(path string) string {
	if body, err := readHead(filepath.Join(path, "flake.nix"), readmeHeadBytes); err == nil {
		if m := flakeDescRe.FindStringSubmatch(body); m != nil {
			if d := truncateDesc(unescapeNixString(m[1])); d != "" {
				return d
			}
		}
	}
	for _, name := range []string{"README.md", "README", "README.rst", "README.txt"} {
		body, err := readHead(filepath.Join(path, name), readmeHeadBytes)
		if err != nil {
			continue
		}
		if d := firstProseLine(body); d != "" {
			return d
		}
	}
	return ""
}

// firstProseLine returns the first line of a README that reads as a sentence
// about the project, skipping headings, badges, HTML, blockquotes, list items
// and code fences. Returns "" when the head contains no such line.
func firstProseLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Headings, badges/images, links-only lines, HTML, quotes, lists,
		// fences and underlines are all structure rather than description.
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") ||
			strings.HasPrefix(line, "<") || strings.HasPrefix(line, ">") ||
			strings.HasPrefix(line, "```") || strings.HasPrefix(line, "- ") ||
			strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "=") ||
			strings.HasPrefix(line, "--") || strings.HasPrefix(line, "[") {
			continue
		}
		line = mdLinkRe.ReplaceAllString(line, "$1")
		line = strings.NewReplacer("`", "", "**", "", "*", "", "_", "").Replace(line)
		line = strings.Join(strings.Fields(line), " ")
		// Very short fragments are almost always stray markup, not prose.
		if len(line) < 10 {
			continue
		}
		return truncateDesc(line)
	}
	return ""
}

// unescapeNixString resolves the backslash escapes a Nix double-quoted string
// may carry, so a scraped description reads as its authored text.
func unescapeNixString(s string) string {
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, " ", `\t`, " ", `\r`, " ").Replace(s)
}

// truncateDesc collapses whitespace and caps a description's length, cutting on
// a word boundary so the ellipsis never lands mid-word.
func truncateDesc(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxDescLen {
		return s
	}
	cut := s[:maxDescLen]
	if i := strings.LastIndex(cut, " "); i > maxDescLen/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " .,;:") + "…"
}

// readHead returns the first n bytes of a file.
func readHead(path string, n int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // read-only; a failed close cannot affect the parsed head
	b, err := io.ReadAll(io.LimitReader(f, n))
	if err != nil && len(b) == 0 {
		return "", err
	}
	return string(b), nil
}
