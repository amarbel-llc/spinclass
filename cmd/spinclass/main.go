package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/server"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/transport"
	"github.com/amarbel-llc/spinclass/internal/clean"
	spinclose "github.com/amarbel-llc/spinclass/internal/close"
	"github.com/amarbel-llc/spinclass/internal/completions"
	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/hooks"
	"github.com/amarbel-llc/spinclass/internal/mcptools"
	"github.com/amarbel-llc/spinclass/internal/merge"
	"github.com/amarbel-llc/spinclass/internal/perms"
	"github.com/amarbel-llc/spinclass/internal/pr"
	"github.com/amarbel-llc/spinclass/internal/prompt"
	"github.com/amarbel-llc/spinclass/internal/pull"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/shop"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/validate"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

var (
	outputFormat      string
	verbose           bool
	startMergeOnClose bool
	startNoAttach     bool
	startPR           string
	startIssue        string
	resumeNoAttach    bool
	mergeGitSync      bool
	closeForce        bool
)

var rootCmd = &cobra.Command{
	Use:   "spinclass",
	Short: "Shell-agnostic git worktree session manager",
	Long:  `spinclass manages git worktree lifecycles: creating worktrees + sessions, and offering close workflows (rebase, merge, cleanup, push).`,
}

var startCmd = &cobra.Command{
	Use:   "start [description...]",
	Short: "Create and start a new worktree session",
	Long:  `Create a new worktree with a random branch name and start a session. Words after "start" are joined as a freeform session description.`,
	Args:  cobra.MinimumNArgs(0),
	RunE: func(cmd *cobra.Command, args []string) error {
		format := outputFormat
		if format == "" {
			format = "tap"
		}

		cwd, err := os.Getwd()
		if err != nil {
			return err
		}

		repoPath, err := worktree.DetectRepo(cwd)
		if err != nil {
			return err
		}

		var resolvedPath worktree.ResolvedPath

		if startPR != "" {
			prInfo, err := pr.Resolve(startPR, repoPath)
			if err != nil {
				return err
			}

			branch := prInfo.HeadRefName

			if !git.BranchExists(repoPath, branch) {
				if _, err := git.Run(repoPath, "fetch", "origin", branch); err != nil {
					return fmt.Errorf("fetching PR branch %q: %w", branch, err)
				}
			}

			absPath := filepath.Join(repoPath, worktree.WorktreesDir, branch)
			repoDirname := filepath.Base(repoPath)

			description := fmt.Sprintf("%s (#%d)", prInfo.Title, prInfo.Number)
			if len(args) > 0 {
				description = strings.Join(args, " ")
			}

			resolvedPath = worktree.ResolvedPath{
				AbsPath:        absPath,
				RepoPath:       repoPath,
				SessionKey:     repoDirname + "/" + branch,
				Branch:         branch,
				Description:    description,
				ExistingBranch: branch,
			}

			prData, prErr := prompt.FetchPR(startPR, repoPath)
			if prErr == nil {
				resolvedPath.PR = &prData
			}
		} else {
			var err error
			resolvedPath, err = worktree.ResolvePath(repoPath, args)
			if err != nil {
				return err
			}
		}

		if startIssue != "" {
			issueData, err := prompt.FetchIssue(startIssue, repoPath)
			if err != nil {
				return fmt.Errorf("fetching issue: %w", err)
			}
			resolvedPath.Issue = &issueData
		}

		hierarchy, err := sweatfile.LoadWorktreeHierarchy(
			os.Getenv("HOME"), repoPath, resolvedPath.AbsPath,
		)
		if err != nil {
			hierarchy, err = sweatfile.LoadHierarchy(os.Getenv("HOME"), repoPath)
			if err != nil {
				return err
			}
		}

		entrypoint := hierarchy.Merged.SessionStart()

		exec := executor.SessionExecutor{
			Entrypoint:  entrypoint,
			Description: resolvedPath.Description,
		}

		return shop.Attach(
			os.Stdout,
			exec,
			resolvedPath,
			format,
			startMergeOnClose,
			startNoAttach,
			verbose,
		)
	},
}

var resumeCmd = &cobra.Command{
	Use:   "resume [id]",
	Short: "Resume an existing worktree session",
	Long:  `Resume an existing worktree session. With no arguments, auto-detects the session from the current working directory. With one argument, resumes the session identified by the worktree directory name (the name under .worktrees/).`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		format := outputFormat
		if format == "" {
			format = "tap"
		}

		var state *session.State
		var err error

		if len(args) == 1 {
			state, err = session.FindByID(args[0])
		} else {
			cwd, cwdErr := os.Getwd()
			if cwdErr != nil {
				return cwdErr
			}
			state, err = session.FindByWorktreePath(cwd)
			if err != nil {
				repoPath, repoErr := worktree.DetectRepo(cwd)
				if repoErr != nil {
					return err
				}
				state, err = chooseSession(repoPath)
			}
		}
		if err != nil {
			return err
		}

		hierarchy, err := sweatfile.LoadWorktreeHierarchy(
			os.Getenv("HOME"), state.RepoPath, state.WorktreePath,
		)
		if err != nil {
			hierarchy, err = sweatfile.LoadHierarchy(os.Getenv("HOME"), state.RepoPath)
			if err != nil {
				return err
			}
		}

		merged := hierarchy.Merged
		entrypoint := merged.SessionStart()
		if resume := merged.SessionResume(); resume != nil {
			entrypoint = resume
		}

		rp := worktree.ResolvedPath{
			AbsPath:     state.WorktreePath,
			RepoPath:    state.RepoPath,
			SessionKey:  state.SessionKey,
			Branch:      state.Branch,
			Description: state.Description,
		}

		exec := executor.SessionExecutor{
			Entrypoint: entrypoint,
		}

		return shop.Attach(
			os.Stdout,
			exec,
			rp,
			format,
			false,          // mergeOnClose
			resumeNoAttach, // noAttach
			verbose,
		)
	},
}

func chooseSession(repoPath string) (*session.State, error) {
	sessions, err := session.ListForRepo(repoPath)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("no sessions found for %s", filepath.Base(repoPath))
	}

	interactive := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
	if !interactive {
		var ids []string
		for _, s := range sessions {
			ids = append(ids, filepath.Base(s.WorktreePath))
		}
		return nil, fmt.Errorf("no session found for current directory; available sessions: %s\nUse: spinclass resume <id>", strings.Join(ids, ", "))
	}

	options := make([]huh.Option[int], len(sessions))
	for i, s := range sessions {
		label := fmt.Sprintf("%s [%s]", s.Branch, s.ResolveState())
		if s.Description != "" {
			label = fmt.Sprintf("%s — %s [%s]", s.Branch, s.Description, s.ResolveState())
		}
		options[i] = huh.NewOption(label, i)
	}

	var selected int
	err = huh.NewSelect[int]().
		Title("Select session to resume").
		Options(options...).
		Value(&selected).
		Run()
	if err != nil {
		return nil, fmt.Errorf("session selection cancelled")
	}

	return &sessions[selected], nil
}

var mergeCmd = &cobra.Command{
	Use:   "merge [target]",
	Short: "Merge a worktree into main",
	Long:  `Merge a worktree branch into the main repo with --ff-only and remove the worktree. When run from inside a worktree, merges that worktree. When run from the main repo, specify a target or choose interactively.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		format := outputFormat
		if format == "" {
			format = "tap"
		}

		var target string
		if len(args) == 1 {
			target = args[0]
		}

		return merge.Run(
			executor.ShellExecutor{},
			format,
			target,
			mergeGitSync,
			verbose,
		)
	},
}

var closeCmd = &cobra.Command{
	Use:   "close [target]",
	Short: "Close a session without merging",
	Long:  `Remove a worktree and its branch without merging into main. Prompts for confirmation if the branch has not been pushed upstream. Use --force to skip.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		format := outputFormat
		if format == "" {
			format = "tap"
		}

		var target string
		if len(args) == 1 {
			target = args[0]
		}

		return spinclose.Run(os.Stdout, target, closeForce, format)
	},
}

var cleanInteractive bool

var pullDirty bool

var pullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull repos and rebase worktrees",
	Long:  `Pull all clean repos, then rebase all clean worktrees onto their repo's default branch. Use -d to include dirty repos and worktrees.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		format := outputFormat
		if format == "" {
			format = "tap"
		}

		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		return pull.Run(cwd, pullDirty, format)
	},
}

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove merged worktrees",
	Long:  `Scan all worktrees, identify those whose branches are fully merged into the main branch, and remove them. Use -i to interactively handle dirty worktrees.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}

		format := outputFormat
		if format == "" {
			format = "tap"
		}

		return clean.Run(cwd, cleanInteractive, format)
	},
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List tracked sessions",
	Long:  `List all tracked sessions from the state directory.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		states, err := session.ListAll()
		if err != nil {
			return err
		}
		for _, s := range states {
			resolved := s.ResolveState()
			fmt.Printf("%s\t%s\t%s\t%s\n", s.SessionKey, resolved, s.WorktreePath, s.Description)
		}
		return nil
	},
}

var updateDescriptionID string

var updateDescriptionCmd = &cobra.Command{
	Use:   "update-description [description...]",
	Short: "Update the description of a session",
	Long:  `Update the freeform description of an existing session. With --id, targets a specific worktree by directory name. Without --id, auto-detects from the current working directory.`,
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var state *session.State
		var err error

		if updateDescriptionID != "" {
			state, err = session.FindByID(updateDescriptionID)
		} else {
			cwd, cwdErr := os.Getwd()
			if cwdErr != nil {
				return cwdErr
			}
			state, err = session.FindByWorktreePath(cwd)
		}
		if err != nil {
			return err
		}

		state.Description = strings.Join(args, " ")
		return session.Write(*state)
	},
}

var (
	completionsSessions bool
	completionsPRs      bool
)

var completionsCmd = &cobra.Command{
	Use:    "completions",
	Short:  "Generate tab-separated completions",
	Long:   `Output tab-separated completion entries for shell integration. Use --sessions to list from session state directory instead of scanning local worktrees.`,
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}

		if completionsSessions {
			repoPath, _ := worktree.DetectRepo(cwd)
			completions.Sessions(os.Stdout, repoPath)
			return nil
		}

		if completionsPRs {
			repoPath, _ := worktree.DetectRepo(cwd)
			completions.PRs(os.Stdout, repoPath)
			return nil
		}

		completions.Local(cwd, os.Stdout)
		return nil
	},
}

var forkFromDir string

var forkCmd = &cobra.Command{
	Use:   "fork [<new-branch>]",
	Short: "Fork current worktree into a new branch",
	Long:  `Create a new worktree branched from the current worktree's HEAD. If new-branch is omitted, a name is auto-generated as <current-branch>-N. Resolves the source worktree from the current directory or --from flag. Does not attach to the new session.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sourceDir := forkFromDir
		if sourceDir == "" {
			var err error
			sourceDir, err = os.Getwd()
			if err != nil {
				return err
			}
		}

		repoPath, err := worktree.DetectRepo(sourceDir)
		if err != nil {
			return err
		}

		currentBranch, err := git.BranchCurrent(sourceDir)
		if err != nil {
			return fmt.Errorf("could not determine current branch in %s: %w", sourceDir, err)
		}

		currentPath := filepath.Join(
			repoPath,
			worktree.WorktreesDir,
			currentBranch,
		)

		if _, err := os.Stat(currentPath); os.IsNotExist(err) {
			return fmt.Errorf(
				"worktree path %s does not exist; fork requires a standard .worktrees layout",
				currentPath,
			)
		}

		sessionKey := filepath.Base(repoPath) + "/" + currentBranch
		rp := worktree.ResolvedPath{
			AbsPath:    currentPath,
			RepoPath:   repoPath,
			Branch:     currentBranch,
			SessionKey: sessionKey,
		}

		var newBranch string
		if len(args) == 1 {
			newBranch = args[0]
		}

		format := outputFormat
		if format == "" {
			format = "tap"
		}

		return shop.Fork(os.Stdout, rp, newBranch, format)
	},
}

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate the sweatfile hierarchy",
	Long:  `Walk the sweatfile hierarchy from PWD and validate each file for structural and semantic correctness. Outputs TAP-14 with subtests.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}

		exitCode := validate.Run(os.Stdout, home, cwd)
		if exitCode != 0 {
			os.Exit(exitCode)
		}
		return nil
	},
}

var cmdExecClaude = &cobra.Command{
	Use:                "exec-claude [claude args...]",
	Short:              "Executes claude after applying sweatfile settings",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		hierarchy, err := sweatfile.LoadDefaultHierarchy()
		if err != nil {
			return err
		}

		return hierarchy.Merged.ExecClaude(args...)
	},
}

var serveCmd = &cobra.Command{
	Use:   "serve-mcp",
	Short: "Start MCP server on stdio",
	Long:  `Start a JSON-RPC MCP server on stdin/stdout. Intended to be launched by an MCP client such as Claude Code via .mcp.json.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		app := mcptools.RegisterAll()

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		t := transport.NewStdio(os.Stdin, os.Stdout)
		registry := server.NewToolRegistryV1()
		app.RegisterMCPToolsV1(registry)

		srv, err := server.New(t, server.Options{
			ServerName:    app.Name,
			ServerVersion: app.Version,
			Instructions:  "Git worktree session manager. Use the merge tool to merge a worktree branch into the default branch.",
			Tools:         registry,
		})
		if err != nil {
			log.Fatalf("creating server: %v", err)
		}

		if err := srv.Run(ctx); err != nil {
			log.Fatalf("server error: %v", err)
		}

		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(
		&outputFormat,
		"format",
		"",
		"output format: tap or table",
	)
	rootCmd.PersistentFlags().BoolVarP(
		&verbose,
		"verbose",
		"v",
		false,
		"show detailed output (YAML diagnostics on passing test points)",
	)
	startCmd.Flags().BoolVar(
		&startMergeOnClose,
		"merge-on-close",
		false,
		"auto-merge worktree into default branch on session close",
	)
	startCmd.Flags().BoolVar(
		&startNoAttach,
		"no-attach",
		false,
		"create worktree but skip attaching (show command that would run)",
	)
	startCmd.Flags().StringVar(
		&startPR,
		"pr",
		"",
		"start session from a PR (number or GitHub URL)",
	)
	startCmd.Flags().StringVar(
		&startIssue,
		"issue",
		"",
		"start session with GitHub issue context (number)",
	)
	startCmd.MarkFlagsMutuallyExclusive("issue", "pr")
	resumeCmd.Flags().BoolVar(
		&resumeNoAttach,
		"no-attach",
		false,
		"find session but skip attaching (show command that would run)",
	)
	mergeCmd.Flags().BoolVar(
		&mergeGitSync,
		"git-sync",
		false,
		"pull and push after merge",
	)
	cleanCmd.Flags().BoolVarP(
		&cleanInteractive,
		"interactive",
		"i",
		false,
		"interactively discard changes in dirty merged worktrees",
	)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(resumeCmd)
	updateDescriptionCmd.Flags().StringVar(
		&updateDescriptionID,
		"id",
		"",
		"worktree ID to update (auto-detects from cwd if omitted)",
	)
	rootCmd.AddCommand(updateDescriptionCmd)
	rootCmd.AddCommand(mergeCmd)
	closeCmd.Flags().BoolVarP(
		&closeForce,
		"force",
		"f",
		false,
		"skip confirmation for unpushed branches",
	)
	rootCmd.AddCommand(closeCmd)
	rootCmd.AddCommand(cleanCmd)
	rootCmd.AddCommand(listCmd)
	completionsCmd.Flags().BoolVar(
		&completionsSessions,
		"sessions",
		false,
		"list completions from session state directory",
	)
	completionsCmd.Flags().BoolVar(
		&completionsPRs,
		"prs",
		false,
		"list open pull requests for completion",
	)
	rootCmd.AddCommand(completionsCmd)
	pullCmd.Flags().BoolVarP(
		&pullDirty,
		"dirty",
		"d",
		false,
		"include dirty repos and worktrees",
	)
	rootCmd.AddCommand(pullCmd)
	rootCmd.AddCommand(perms.NewPermsCmd())
	rootCmd.AddCommand(hooks.NewHooksCmd())
	forkCmd.Flags().StringVar(
		&forkFromDir,
		"from",
		"",
		"source worktree directory to fork from",
	)
	rootCmd.AddCommand(forkCmd)
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(cmdExecClaude)
	rootCmd.AddCommand(serveCmd)
}

func main() {
	rootCmd.Use = filepath.Base(os.Args[0])
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
