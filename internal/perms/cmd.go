package perms

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

// RunListWriter writes the global tier and either a single repo's tier (if
// repo != "") or every non-empty repo tier under TiersDir() to w.
func RunListWriter(w io.Writer, repo string) error {
	tiersDir := TiersDir()

	globalPath := filepath.Join(tiersDir, "global.json")
	globalTier, err := LoadTierFile(globalPath)
	if err != nil {
		return fmt.Errorf("loading global tier: %w", err)
	}

	fmt.Fprintln(w, "Global tier:")
	if len(globalTier.Allow) == 0 {
		fmt.Fprintln(w, "  (empty)")
	} else {
		for _, rule := range globalTier.Allow {
			fmt.Fprintf(w, "  %s\n", rule)
		}
	}

	if repo != "" {
		repoPath := filepath.Join(tiersDir, "repos", repo+".json")
		repoTier, err := LoadTierFile(repoPath)
		if err != nil {
			return fmt.Errorf("loading repo tier %s: %w", repo, err)
		}

		fmt.Fprintf(w, "\nRepo tier (%s):\n", repo)
		if len(repoTier.Allow) == 0 {
			fmt.Fprintln(w, "  (empty)")
		} else {
			for _, rule := range repoTier.Allow {
				fmt.Fprintf(w, "  %s\n", rule)
			}
		}

		return nil
	}

	reposDir := filepath.Join(tiersDir, "repos")
	entries, err := os.ReadDir(reposDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		repoName := strings.TrimSuffix(entry.Name(), ".json")
		repoTier, err := LoadTierFile(filepath.Join(reposDir, entry.Name()))
		if err != nil {
			continue
		}

		if len(repoTier.Allow) == 0 {
			continue
		}

		fmt.Fprintf(w, "\nRepo tier (%s):\n", repoName)
		for _, rule := range repoTier.Allow {
			fmt.Fprintf(w, "  %s\n", rule)
		}
	}

	return nil
}

// RunListString runs RunListWriter into a buffer and returns the output.
func RunListString(repo string) (string, error) {
	var buf bytes.Buffer
	if err := RunListWriter(&buf, repo); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// RunEdit opens the appropriate tier file in $EDITOR. If global is true,
// edits the global tier. If repo is non-empty, edits that repo's tier.
// Otherwise edits global.
func RunEdit(global bool, repo string) error {
	tiersDir := TiersDir()

	var tierPath string
	switch {
	case global:
		tierPath = filepath.Join(tiersDir, "global.json")
	case repo != "":
		tierPath = filepath.Join(tiersDir, "repos", repo+".json")
	default:
		tierPath = filepath.Join(tiersDir, "global.json")
	}

	if _, err := os.Stat(tierPath); os.IsNotExist(err) {
		if err := SaveTierFile(tierPath, Tier{Allow: []string{}}); err != nil {
			return fmt.Errorf("creating tier file: %w", err)
		}
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	editorCmd := exec.Command(editor, tierPath)
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = os.Stdout
	editorCmd.Stderr = os.Stderr

	return editorCmd.Run()
}

// RunReview resolves the worktree path and repo name, then delegates to
// RunReviewEditor for the interactive review loop.
func RunReview(worktreePath string, dryRun bool) error {
	if !filepath.IsAbs(worktreePath) {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		worktreePath = filepath.Join(cwd, worktreePath)
	}

	repoPath, err := worktree.DetectRepo(worktreePath)
	if err != nil {
		return fmt.Errorf("could not detect repo: %w", err)
	}
	repoName := filepath.Base(repoPath)

	return RunReviewEditor(worktreePath, repoName, dryRun)
}

// RunReviewEditor opens $EDITOR with reviewable rules and loops until the user
// accepts, edits again, or aborts.
func RunReviewEditor(worktreePath, repoName string, dryRun bool) error {
	branch, err := git.BranchCurrent(worktreePath)
	if err != nil {
		return fmt.Errorf("could not detect branch: %w", err)
	}
	logPath := ToolUseLogPath(repoName, branch)
	tiersDir := TiersDir()
	globalSettingsPath := GlobalClaudeSettingsPath()

	rules, err := ComputeReviewableRules(
		logPath, globalSettingsPath, tiersDir, repoName, worktreePath,
	)
	if err != nil {
		return err
	}

	if len(rules) == 0 {
		fmt.Println("no new permissions to review")
		return nil
	}

	content := FormatEditorContent(rules, repoName)

	tmpFile, err := os.CreateTemp("", "spinclass-perms-review-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	for {
		if err := openEditor(tmpFile.Name()); err != nil {
			return fmt.Errorf("editor failed: %w", err)
		}

		edited, err := os.ReadFile(tmpFile.Name())
		if err != nil {
			return err
		}

		decisions, err := ParseEditorContent(string(edited))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Parse error: %v\nRe-opening editor.\n", err)
			continue
		}

		if len(decisions) == 0 {
			fmt.Println("no decisions — aborting")
			return nil
		}

		fmt.Println()
		for _, d := range decisions {
			friendly := FriendlyName(d.Rule)
			if friendly != "" {
				fmt.Printf("  %-8s %s  # %s\n", d.Action, d.Rule, friendly)
			} else {
				fmt.Printf("  %-8s %s\n", d.Action, d.Rule)
			}
		}
		fmt.Println()

		var choice string
		prompt := huh.NewSelect[string]().
			Title("Review complete").
			Options(
				huh.NewOption("Accept", "accept"),
				huh.NewOption("Edit again", "edit"),
				huh.NewOption("Abort", "abort"),
			).
			Value(&choice)

		if err := prompt.Run(); err != nil {
			return err
		}

		switch choice {
		case "accept":
			if dryRun {
				DryRunDecisions(os.Stdout, tiersDir, repoName, decisions)
				return nil
			}
			return RouteDecisions(tiersDir, repoName, decisions)
		case "edit":
			continue
		case "abort":
			fmt.Println("aborted")
			return nil
		}
	}
}

// RunReviewEditorAll opens $EDITOR with reviewable rules merged across every
// session's tool-use log. The 'repo' action is rejected because there is no
// single repo context; rules can only be promoted to the global tier.
func RunReviewEditorAll(dryRun bool) error {
	tiersDir := TiersDir()
	globalSettingsPath := GlobalClaudeSettingsPath()

	rules, err := ComputeReviewableRulesAll(tiersDir, globalSettingsPath)
	if err != nil {
		return err
	}

	if len(rules) == 0 {
		fmt.Println("no new permissions to review across any session")
		return nil
	}

	content := FormatEditorContentAll(rules)

	tmpFile, err := os.CreateTemp("", "spinclass-perms-review-all-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	for {
		if err := openEditor(tmpFile.Name()); err != nil {
			return fmt.Errorf("editor failed: %w", err)
		}

		edited, err := os.ReadFile(tmpFile.Name())
		if err != nil {
			return err
		}

		decisions, err := ParseEditorContent(string(edited))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Parse error: %v\nRe-opening editor.\n", err)
			continue
		}

		if invalid := firstRepoDecision(decisions); invalid != "" {
			fmt.Fprintf(os.Stderr, "'repo' action is not valid in --all mode (rule: %s)\nRe-opening editor.\n", invalid)
			continue
		}

		if len(decisions) == 0 {
			fmt.Println("no decisions — aborting")
			return nil
		}

		fmt.Println()
		for _, d := range decisions {
			friendly := FriendlyName(d.Rule)
			if friendly != "" {
				fmt.Printf("  %-8s %s  # %s\n", d.Action, d.Rule, friendly)
			} else {
				fmt.Printf("  %-8s %s\n", d.Action, d.Rule)
			}
		}
		fmt.Println()

		var choice string
		prompt := huh.NewSelect[string]().
			Title("Review complete").
			Options(
				huh.NewOption("Accept", "accept"),
				huh.NewOption("Edit again", "edit"),
				huh.NewOption("Abort", "abort"),
			).
			Value(&choice)

		if err := prompt.Run(); err != nil {
			return err
		}

		switch choice {
		case "accept":
			if dryRun {
				DryRunDecisions(os.Stdout, tiersDir, "", decisions)
				return nil
			}
			return RouteDecisions(tiersDir, "", decisions)
		case "edit":
			continue
		case "abort":
			fmt.Println("aborted")
			return nil
		}
	}
}

// firstRepoDecision returns the first rule whose action is ReviewPromoteRepo,
// or "" if none. Used to reject 'repo' decisions in --all mode.
func firstRepoDecision(decisions []ReviewDecision) string {
	for _, d := range decisions {
		if d.Action == ReviewPromoteRepo {
			return d.Rule
		}
	}
	return ""
}

func openEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
