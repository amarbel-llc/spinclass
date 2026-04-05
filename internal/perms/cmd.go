package perms

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

func NewPermsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "perms",
		Short: "Manage Claude Code permission tiers",
	}

	cmd.AddCommand(newCheckCmd())
	cmd.AddCommand(newReviewCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newEditCmd())

	return cmd
}

func newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "check",
		Short:  "Handle a PermissionRequest hook",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunCheck(os.Stdin, os.Stdout, TiersDir())
		},
	}
}

func newReviewCmd() *cobra.Command {
	var worktreeDir string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "review [worktree-path]",
		Short: "Interactively review new permissions from a session",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var worktreePath string

			switch {
			case worktreeDir != "":
				worktreePath = worktreeDir
			case len(args) > 0:
				worktreePath = args[0]
			default:
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				worktreePath = cwd
			}

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
		},
	}

	cmd.Flags().StringVar(&worktreeDir, "worktree-dir", "", "override worktree path")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing")

	return cmd
}

func newListCmd() *cobra.Command {
	var repo string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List permission tier rules",
		RunE: func(cmd *cobra.Command, args []string) error {
			tiersDir := TiersDir()

			globalPath := filepath.Join(tiersDir, "global.json")
			globalTier, err := LoadTierFile(globalPath)
			if err != nil {
				return fmt.Errorf("loading global tier: %w", err)
			}

			fmt.Println("Global tier:")
			if len(globalTier.Allow) == 0 {
				fmt.Println("  (empty)")
			} else {
				for _, rule := range globalTier.Allow {
					fmt.Printf("  %s\n", rule)
				}
			}

			if repo != "" {
				repoPath := filepath.Join(tiersDir, "repos", repo+".json")
				repoTier, err := LoadTierFile(repoPath)
				if err != nil {
					return fmt.Errorf("loading repo tier %s: %w", repo, err)
				}

				fmt.Printf("\nRepo tier (%s):\n", repo)
				if len(repoTier.Allow) == 0 {
					fmt.Println("  (empty)")
				} else {
					for _, rule := range repoTier.Allow {
						fmt.Printf("  %s\n", rule)
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

				fmt.Printf("\nRepo tier (%s):\n", repoName)
				for _, rule := range repoTier.Allow {
					fmt.Printf("  %s\n", rule)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "show rules for a specific repo only")

	return cmd
}

func newEditCmd() *cobra.Command {
	var global bool
	var repo string

	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit a permission tier file in $EDITOR",
		RunE: func(cmd *cobra.Command, args []string) error {
			tiersDir := TiersDir()

			var tierPath string
			if global {
				tierPath = filepath.Join(tiersDir, "global.json")
			} else if repo != "" {
				tierPath = filepath.Join(tiersDir, "repos", repo+".json")
			} else {
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
		},
	}

	cmd.Flags().BoolVar(&global, "global", false, "edit the global tier file")
	cmd.Flags().StringVar(&repo, "repo", "", "edit a repo-specific tier file")

	return cmd
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

		// Print the parsed decisions for review
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
