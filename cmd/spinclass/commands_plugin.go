package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/prompt"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

// execStartOutput is the JSON schema returned by an exec-start command.
type execStartOutput struct {
	Branch      string `json:"branch,omitempty"`
	Description string `json:"description,omitempty"`
	Context     string `json:"context"`
}

// registerPluginStartCommands loads the sweatfile hierarchy for the current
// working directory and registers a `start-<name>` subcommand for each entry
// in `[[start-commands]]`. On any load error it returns silently so `sc` stays
// usable outside of a repo. Registration happens after the built-in session
// commands and skips names that are already registered to preserve built-in
// behaviour.
func registerPluginStartCommands(app *command.App) {
	merged, ok := loadMergedSweatfile()
	if !ok {
		return
	}
	for _, sc := range merged.StartCommands {
		if sc.Name == "" || len(sc.ExecStart) == 0 {
			continue
		}
		cmdName := "start-" + sc.Name
		if _, exists := app.GetCommand(cmdName); exists {
			continue
		}
		app.AddCommand(buildPluginCommand(cmdName, sc))
	}
}

// loadMergedSweatfile loads the sweatfile hierarchy from cwd with the
// baseline (`GetDefault()`) merged on top, so built-in entries surface even
// when the user has no repo/global sweatfile.
func loadMergedSweatfile() (sweatfile.Sweatfile, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return sweatfile.Sweatfile{}, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return sweatfile.Sweatfile{}, false
	}
	var merged sweatfile.Sweatfile
	if repoPath, err := worktree.DetectRepo(cwd); err == nil {
		hierarchy, err := sweatfile.LoadHierarchy(home, repoPath)
		if err != nil {
			return sweatfile.Sweatfile{}, false
		}
		merged = hierarchy.Merged
	} else {
		// Outside a repo: load only the global sweatfile (LoadHierarchy with
		// home as the repoDir walks zero intermediate levels, so it just
		// reads ~/.config/spinclass/sweatfile).
		hierarchy, err := sweatfile.LoadHierarchy(home, home)
		if err != nil {
			return sweatfile.Sweatfile{}, false
		}
		merged = hierarchy.Merged
	}
	merged = sweatfile.GetDefault().MergeWith(merged)
	return merged, true
}

func buildPluginCommand(cmdName string, sc sweatfile.StartCommand) *command.Command {
	argName := sc.ArgName
	if argName == "" {
		argName = "arg"
	}
	shortDesc := sc.Description
	if shortDesc == "" {
		shortDesc = "Start a session via the " + sc.Name + " plugin"
	}

	longDesc := fmt.Sprintf(
		"Config-driven start command declared via [[start-commands]] in the sweatfile. "+
			"Runs the exec-start command with {arg} replaced by the positional argument, "+
			"parses the JSON output, creates a worktree session, and writes the context "+
			"fragment to .spinclass/system_prompt_append.d/3-start-%s.md.\n\n"+
			"The exec-start command must produce JSON on stdout with the schema:\n\n"+
			"  {\"branch\": \"<optional>\", \"description\": \"<optional>\", \"context\": \"<string>\"}\n\n"+
			"When branch is set, an existing local or remote branch is checked out "+
			"(like start-gh_pr) instead of creating a new one. When description is set, "+
			"it becomes the session description unless --description is passed. The context "+
			"value is written as the session's system prompt fragment.\n\n"+
			"See spinclass-start-commands(7) for the full plugin authoring guide.",
		sc.Name,
	)

	cmd := &command.Command{
		Name: cmdName,
		Description: command.Description{
			Short: shortDesc,
			Long:  longDesc,
		},
		Params: []command.Param{
			{
				Name:        argName,
				Type:        command.String,
				Description: sc.ArgHelp,
				Required:    true,
				Completer:   pluginCompleter(sc),
			},
			{Name: "description", Type: command.String, Description: "Override the default session description"},
			{Name: "merge-on-close", Type: command.Bool, Description: "Auto-merge worktree into default branch on session close"},
			{Name: "no-attach", Type: command.Bool, Description: "Create worktree but skip attaching"},
		},
		Files: []command.FilePath{
			{
				Path:        fmt.Sprintf(".spinclass/system_prompt_append.d/3-start-%s.md", sc.Name),
				Description: "System prompt fragment written from the exec-start context field.",
			},
		},
		RunCLI: makePluginRunCLI(sc, argName),
	}

	cmd.Examples = []command.Example{
		{
			Description: fmt.Sprintf("Start a session with %s", sc.Name),
			Command:     fmt.Sprintf("sc %s <value>", cmdName),
		},
		{
			Description: "Create worktree without attaching",
			Command:     fmt.Sprintf("sc %s --no-attach <value>", cmdName),
		},
	}

	return cmd
}

// execCompletionEntry is the JSON schema for a single completion item
// returned by an exec-completions command.
type execCompletionEntry struct {
	Arg         string `json:"arg"`
	Description string `json:"description"`
}

// pluginCompleter returns a Completer that execs the user's `exec-completions`
// command and parses stdout as JSON: [{"arg": "...", "description": "..."}].
// Any error returns nil to match the defensive style of completeGHPRs.
func pluginCompleter(sc sweatfile.StartCommand) func() map[string]string {
	if len(sc.ExecCompletions) == 0 {
		return nil
	}
	return func() map[string]string {
		cmd := exec.Command(sc.ExecCompletions[0], sc.ExecCompletions[1:]...)
		out, err := cmd.Output()
		if err != nil {
			return nil
		}
		var entries []execCompletionEntry
		if err := json.Unmarshal(out, &entries); err != nil {
			return nil
		}
		if len(entries) == 0 {
			return nil
		}
		result := make(map[string]string, len(entries))
		for _, e := range entries {
			result[e.Arg] = e.Description
		}
		return result
	}
}

// makePluginRunCLI returns the RunCLI closure for a config-declared
// `start-<name>` command. It validates the argument, runs the user's
// `exec-start` command, parses JSON output (branch, description, context),
// and delegates to the standard `attachSession` flow. When the output
// includes a `branch` field, the command checks out an existing branch
// (mirroring `start-gh_pr` behaviour) instead of creating a new one.
func makePluginRunCLI(
	sc sweatfile.StartCommand, argName string,
) func(context.Context, json.RawMessage) error {
	return func(_ context.Context, args json.RawMessage) error {
		// Dynamic arg lookup: the JSON key matches sc.ArgName, which is
		// user-provided, so we unmarshal into a map instead of a struct.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(args, &raw); err != nil {
			return fmt.Errorf("start-%s: parsing arguments: %w", sc.Name, err)
		}

		var p startArgs
		if err := json.Unmarshal(args, &p); err != nil {
			return fmt.Errorf("start-%s: parsing arguments: %w", sc.Name, err)
		}

		var argValue string
		if v, ok := raw[argName]; ok {
			if err := json.Unmarshal(v, &argValue); err != nil {
				return fmt.Errorf("start-%s: parsing %s: %w", sc.Name, argName, err)
			}
		}
		if argValue == "" {
			return fmt.Errorf("start-%s requires a %s argument", sc.Name, argName)
		}

		if sc.ArgRegex != nil && *sc.ArgRegex != "" {
			re, err := regexp.Compile(*sc.ArgRegex)
			if err != nil {
				return fmt.Errorf("start-%s: invalid arg-regex: %w", sc.Name, err)
			}
			if !re.MatchString(argValue) {
				return fmt.Errorf(
					"start-%s: %s %q does not match %s",
					sc.Name, argName, argValue, *sc.ArgRegex,
				)
			}
		}

		rawOutput, err := runPluginExecStart(sc, argValue)
		if err != nil {
			return fmt.Errorf("start-%s exec-start: %w", sc.Name, err)
		}

		var execOut execStartOutput
		if rawOutput != "" {
			if err := json.Unmarshal([]byte(rawOutput), &execOut); err != nil {
				return fmt.Errorf("start-%s: exec-start produced invalid JSON: %w", sc.Name, err)
			}
		}

		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		repoPath, err := worktree.DetectRepo(cwd)
		if err != nil {
			return err
		}

		description := p.Description
		if description == "" && execOut.Description != "" {
			description = execOut.Description
		}
		if description == "" {
			description = fmt.Sprintf("%s: %s", sc.Name, argValue)
		}

		var resolvedPath worktree.ResolvedPath

		if execOut.Branch != "" {
			branch := execOut.Branch
			if !git.BranchExists(repoPath, branch) {
				if git.RemoteBranchExists(repoPath, branch) {
					if _, err := git.Run(repoPath, "fetch", "origin", branch); err != nil {
						return fmt.Errorf("start-%s: fetching branch %q: %w", sc.Name, branch, err)
					}
				} else {
					return fmt.Errorf("start-%s: branch %q not found locally or on origin", sc.Name, branch)
				}
			}
			absPath := filepath.Join(repoPath, worktree.WorktreesDir, branch)
			repoDirname := filepath.Base(repoPath)
			resolvedPath = worktree.ResolvedPath{
				AbsPath:        absPath,
				RepoPath:       repoPath,
				SessionKey:     repoDirname + "/" + branch,
				Branch:         branch,
				Description:    description,
				ExistingBranch: branch,
			}
		} else {
			resolvedPath, err = worktree.ResolvePath(repoPath, []string{description})
			if err != nil {
				return err
			}
		}

		if strings.TrimSpace(execOut.Context) != "" {
			resolvedPath.PluginFragments = []prompt.PluginFragment{{
				Name:    sc.Name,
				Content: execOut.Context,
			}}
		}

		return attachSession(resolvedPath, p)
	}
}

// runPluginExecStart executes the user's `exec-start` command with `{arg}`
// tokens substituted by the positional argument value. Substitution is a
// literal string replace on each argv element — the command is exec'd directly
// (no shell), so there is no injection surface beyond what the user's own argv
// expressed. Users who want shell expansion must wrap in `sh -c`.
// The caller is responsible for parsing the returned stdout as JSON.
func runPluginExecStart(sc sweatfile.StartCommand, argValue string) (string, error) {
	if len(sc.ExecStart) == 0 {
		return "", nil
	}
	argv := make([]string, len(sc.ExecStart))
	for i, token := range sc.ExecStart {
		argv[i] = strings.ReplaceAll(token, "{arg}", argValue)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
