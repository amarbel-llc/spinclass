package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/spinclass/internal/prompt"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

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
		if sc.Name == "" || len(sc.Prompt) == 0 {
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
	merged = merged.MergeWith(sweatfile.GetDefault())
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
	return &command.Command{
		Name: cmdName,
		Description: command.Description{
			Short: shortDesc,
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
		RunCLI: makePluginRunCLI(sc, argName),
	}
}

// pluginCompleter returns a Completer that execs the user's `completion`
// command and parses stdout as tab-separated `value\tdescription` lines.
// Lines without a tab are treated as bare values with no description.
// Any error returns nil to match the defensive style of completeGHPRs.
func pluginCompleter(sc sweatfile.StartCommand) func() map[string]string {
	if len(sc.Completion) == 0 {
		return nil
	}
	return func() map[string]string {
		cmd := exec.Command(sc.Completion[0], sc.Completion[1:]...)
		out, err := cmd.Output()
		if err != nil {
			return nil
		}
		result := make(map[string]string)
		scanner := bufio.NewScanner(bytes.NewReader(out))
		for scanner.Scan() {
			line := strings.TrimRight(scanner.Text(), "\r")
			if line == "" {
				continue
			}
			if tab := strings.IndexByte(line, '\t'); tab >= 0 {
				result[line[:tab]] = line[tab+1:]
			} else {
				result[line] = ""
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	}
}

// makePluginRunCLI returns the RunCLI closure for a config-declared
// `start-<name>` command. It validates the argument, runs the user's
// `prompt` command to capture a markdown fragment, and then delegates to
// the standard `attachSession` flow with a PluginFragment attached to the
// ResolvedPath.
func makePluginRunCLI(
	sc sweatfile.StartCommand, argName string,
) func(context.Context, json.RawMessage) error {
	return func(_ context.Context, args json.RawMessage) error {
		// Dynamic arg lookup: the JSON key matches sc.ArgName, which is
		// user-provided, so we unmarshal into a map instead of a struct.
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(args, &raw)

		var p startArgs
		_ = json.Unmarshal(args, &p)

		var argValue string
		if v, ok := raw[argName]; ok {
			_ = json.Unmarshal(v, &argValue)
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

		content, err := runPluginPrompt(sc, argValue)
		if err != nil {
			return fmt.Errorf("start-%s prompt: %w", sc.Name, err)
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
		if description == "" {
			description = fmt.Sprintf("%s: %s", sc.Name, argValue)
		}

		resolvedPath, err := worktree.ResolvePath(repoPath, []string{description})
		if err != nil {
			return err
		}

		if strings.TrimSpace(content) != "" {
			resolvedPath.PluginFragments = []prompt.PluginFragment{{
				Name:    sc.Name,
				Content: content,
			}}
		}

		return attachSession(resolvedPath, p)
	}
}

// runPluginPrompt executes the user's `prompt` command with `{arg}` tokens
// substituted by the positional argument value. Substitution is a literal
// string replace on each argv element — the command is exec'd directly (no
// shell), so there is no injection surface beyond what the user's own argv
// expressed. Users who want shell expansion must wrap in `sh -c`.
func runPluginPrompt(sc sweatfile.StartCommand, argValue string) (string, error) {
	if len(sc.Prompt) == 0 {
		return "", nil
	}
	argv := make([]string, len(sc.Prompt))
	for i, token := range sc.Prompt {
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
