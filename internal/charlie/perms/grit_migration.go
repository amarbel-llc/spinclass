package perms

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const moxyGritMarker = "moxy__grit_"

// GritFinding is one existing allow rule that names a moxy grit tool.
type GritFinding struct {
	// Tier is "global" or "repo <name>".
	Tier string
	Rule string
	// NewTool is the qualified name of the spinclass tool with the same leaf.
	NewTool string
}

// GritMigrationReport reports, without rewriting anything, every rule in the
// global and repo tier files that names a `...moxy__grit_*` tool (FDR 0036
// section 6). Rules are reported rather than promoted because the new model
// auto-allows less than a blanket allow rule did.
func GritMigrationReport(tiersDir string) ([]GritFinding, error) {
	var findings []GritFinding
	scan := func(tierName, path string) error {
		tier, err := LoadTierFile(path)
		if err != nil {
			return fmt.Errorf("loading %s tier: %w", tierName, err)
		}
		for _, rule := range tier.Allow {
			if leaf, ok := moxyGritLeaf(rule); ok {
				findings = append(findings, GritFinding{Tier: tierName, Rule: rule, NewTool: gritToolPrefix + leaf})
			}
		}
		return nil
	}

	if err := scan("global", filepath.Join(tiersDir, "global.json")); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(tiersDir, "repos"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		if err := scan("repo "+name, filepath.Join(tiersDir, "repos", entry.Name())); err != nil {
			return nil, err
		}
	}
	return findings, nil
}

// moxyGritLeaf extracts the tool leaf ("stash") from a rule naming
// ...moxy__grit_<leaf>, with or without an argument pattern after it.
func moxyGritLeaf(rule string) (string, bool) {
	_, after, found := strings.Cut(rule, moxyGritMarker)
	if !found {
		return "", false
	}
	end := strings.IndexFunc(after, func(r rune) bool {
		return !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
	if end >= 0 {
		after = after[:end]
	}
	if after == "" {
		return "", false
	}
	return after, true
}

// writeGritMigrationReport prints the report section; it prints nothing when
// there are no findings.
func writeGritMigrationReport(w io.Writer, findings []GritFinding) {
	if len(findings) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "\nGrit migration (moxy grit rules found; not rewritten):")
	_, _ = fmt.Fprintln(w, "  spinclass auto-allows less than a blanket moxy grit rule did, so these are reported, not promoted.")
	for _, f := range findings {
		_, _ = fmt.Fprintf(w, "  [%s] %s  ->  %s\n", f.Tier, f.Rule, f.NewTool)
	}
}
