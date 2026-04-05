package perms

import (
	"fmt"
	"io"
	"path/filepath"
)

const (
	ReviewPromoteGlobal = "global"
	ReviewPromoteRepo   = "repo"
	ReviewKeep          = "keep"
	ReviewDiscard       = "discard"
)

type ReviewDecision struct {
	Rule   string
	Action string
}

func RouteDecisions(
	tiersDir, repo string,
	decisions []ReviewDecision,
) error {
	for _, d := range decisions {
		switch d.Action {
		case ReviewPromoteGlobal:
			globalPath := filepath.Join(tiersDir, "global.json")
			if err := AppendToTierFile(globalPath, d.Rule); err != nil {
				return err
			}

		case ReviewPromoteRepo:
			repoPath := filepath.Join(tiersDir, "repos", repo+".json")
			if err := AppendToTierFile(repoPath, d.Rule); err != nil {
				return err
			}

		case ReviewDiscard:
			// Rules are derived from the tool-use log, not stored in
			// settings. Discarding is a no-op — the rule simply isn't
			// promoted.

		case ReviewKeep:
			// Same as discard — the rule stays only in the log.
		}
	}

	return nil
}

// DryRunDecisions prints what RouteDecisions would do without writing files.
func DryRunDecisions(w io.Writer, tiersDir, repo string, decisions []ReviewDecision) {
	groups := map[string][]string{
		ReviewPromoteGlobal: {},
		ReviewPromoteRepo:   {},
		ReviewDiscard:       {},
		ReviewKeep:          {},
	}

	for _, d := range decisions {
		groups[d.Action] = append(groups[d.Action], d.Rule)
	}

	globalPath := filepath.Join(tiersDir, "global.json")
	repoPath := filepath.Join(tiersDir, "repos", repo+".json")

	if len(groups[ReviewPromoteGlobal]) > 0 {
		fmt.Fprintf(w, "would promote to global tier (%s):\n", globalPath)
		for _, r := range groups[ReviewPromoteGlobal] {
			fmt.Fprintf(w, "  %s\n", r)
		}
	}
	if len(groups[ReviewPromoteRepo]) > 0 {
		fmt.Fprintf(w, "would promote to repo tier (%s):\n", repoPath)
		for _, r := range groups[ReviewPromoteRepo] {
			fmt.Fprintf(w, "  %s\n", r)
		}
	}
	if len(groups[ReviewDiscard]) > 0 {
		fmt.Fprintln(w, "would discard (not promoted):")
		for _, r := range groups[ReviewDiscard] {
			fmt.Fprintf(w, "  %s\n", r)
		}
	}
	if len(groups[ReviewKeep]) > 0 {
		fmt.Fprintln(w, "would keep (not promoted):")
		for _, r := range groups[ReviewKeep] {
			fmt.Fprintf(w, "  %s\n", r)
		}
	}
}
