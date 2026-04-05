package perms

import (
	"bufio"
	"fmt"
	"sort"
	"strings"
)

// FriendlyName extracts a short "server:tool" name from an MCP permission
// string like "mcp__plugin_grit_grit__add" → "grit:add" or
// "mcp__glean_default__search" → "glean:search". Returns empty string for
// non-MCP rules.
func FriendlyName(rule string) string {
	// Strip trailing arguments like "mcp__foo__bar(args)"
	base := rule
	if idx := strings.Index(base, "("); idx >= 0 {
		base = base[:idx]
	}

	if !strings.HasPrefix(base, "mcp__") {
		return ""
	}

	// Format: mcp__<server>_<server>__<tool> or mcp__<server>__<tool>
	withoutPrefix := base[len("mcp__"):]
	dunderIdx := strings.LastIndex(withoutPrefix, "__")
	if dunderIdx < 0 {
		return ""
	}

	serverPart := withoutPrefix[:dunderIdx]
	tool := withoutPrefix[dunderIdx+2:]

	// serverPart may be "plugin_grit_grit" or "glean_default"
	// For "plugin_X_X", extract X. For others, take first segment.
	if strings.HasPrefix(serverPart, "plugin_") {
		serverPart = serverPart[len("plugin_"):]
		// serverPart is now "grit_grit" or "get-hubbed_get-hubbed"
		// Take everything before the last underscore
		lastUnderscore := strings.LastIndex(serverPart, "_")
		if lastUnderscore > 0 {
			serverPart = serverPart[:lastUnderscore]
		}
	} else {
		// "glean_default" → "glean"
		if idx := strings.Index(serverPart, "_"); idx > 0 {
			serverPart = serverPart[:idx]
		}
	}

	if serverPart == "" || tool == "" {
		return ""
	}

	return serverPart + ":" + tool
}

// FormatEditorContent generates the editor buffer content for review.
// Rules are sorted alphabetically and default to "discard". MCP rules
// get an inline "# server:tool" comment.
func FormatEditorContent(rules []string, repoName string) string {
	sorted := make([]string, len(rules))
	copy(sorted, rules)
	sort.Strings(sorted)

	var b strings.Builder

	b.WriteString("# spinclass perms review — change the action word for each permission\n")
	b.WriteString("# Actions: global | repo | keep | discard (unique prefixes OK: g/r/k/d)\n")
	b.WriteString("# Lines starting with # are ignored. Empty lines are ignored.\n")
	fmt.Fprintf(&b, "# Repo: %s\n", repoName)
	b.WriteString("\n")

	for _, rule := range sorted {
		friendly := FriendlyName(rule)
		if friendly != "" {
			fmt.Fprintf(&b, "discard %s  # %s\n", rule, friendly)
		} else {
			fmt.Fprintf(&b, "discard %s\n", rule)
		}
	}

	return b.String()
}

// ParseEditorContent parses the editor buffer back into ReviewDecisions.
// Ignores comment lines (starting with #) and blank lines. Supports
// unique action prefixes (g/r/k/d).
func ParseEditorContent(content string) ([]ReviewDecision, error) {
	var decisions []ReviewDecision

	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split on first whitespace: action + rest
		spaceIdx := strings.IndexAny(line, " \t")
		if spaceIdx < 0 {
			return nil, fmt.Errorf("line %d: expected 'action rule', got %q", lineNum, line)
		}

		actionStr := line[:spaceIdx]
		rest := strings.TrimSpace(line[spaceIdx+1:])

		// Strip trailing # comment from the rule
		if commentIdx := strings.LastIndex(rest, "  #"); commentIdx >= 0 {
			rest = strings.TrimSpace(rest[:commentIdx])
		}

		action, err := resolveActionPrefix(actionStr)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}

		decisions = append(decisions, ReviewDecision{
			Rule:   rest,
			Action: action,
		})
	}

	return decisions, scanner.Err()
}

// resolveActionPrefix resolves a full action name or unique prefix to the
// canonical action constant. All four actions have unique first letters.
func resolveActionPrefix(prefix string) (string, error) {
	actions := []string{ReviewPromoteGlobal, ReviewPromoteRepo, ReviewKeep, ReviewDiscard}

	var matches []string
	for _, a := range actions {
		if strings.HasPrefix(a, prefix) {
			matches = append(matches, a)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("unknown action %q (valid: global, repo, keep, discard)", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous action prefix %q (matches: %s)", prefix, strings.Join(matches, ", "))
	}
}
