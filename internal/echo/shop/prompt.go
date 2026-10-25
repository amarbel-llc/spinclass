package shop

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
)

// EnvAutocloseAssume is the env var that lets callers bypass the
// interactive auto-close prompt by pre-committing an answer. Recognized
// values: yes/no/y/n/true/false/1/0 (case-insensitive). When set the
// auto-close branch fires (or doesn't) regardless of TTY state, which
// is what makes the close path testable from bats and other headless
// callers. See #66.
const EnvAutocloseAssume = "SPINCLASS_AUTOCLOSE_ASSUME"

// parseAutocloseAssume returns (proceed, set). set=true only when
// EnvAutocloseAssume contains a recognized truthy/falsy value; an
// unset, empty, or unrecognized value returns (false, false) so the
// caller falls back to the interactive prompt.
func parseAutocloseAssume() (proceed, set bool) {
	raw := os.Getenv(EnvAutocloseAssume)
	if raw == "" {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yes", "y", "true", "1":
		return true, true
	case "no", "n", "false", "0":
		return false, true
	}
	return false, false
}

type dirtyAction string

const (
	actionDiscard  dirtyAction = "Discard changes and merge"
	actionReattach dirtyAction = "Reattach to session"
	actionExit     dirtyAction = "Exit without integrating"
)

func promptDirtyAction(branch string) (dirtyAction, error) {
	var action dirtyAction
	err := huh.NewSelect[dirtyAction]().
		Title("Worktree "+branch+" has uncommitted changes").
		Options(
			huh.NewOption(string(actionDiscard), actionDiscard),
			huh.NewOption(string(actionReattach), actionReattach),
			huh.NewOption(string(actionExit), actionExit),
		).
		Value(&action).
		Run()
	return action, err
}

func promptDefaultBranch() (string, error) {
	var selected string
	err := huh.NewSelect[string]().
		Title("Both main and master branches exist. Which should be the rebase target?").
		Options(
			huh.NewOption("main", "main"),
			huh.NewOption("master", "master"),
		).
		Value(&selected).
		Run()
	return selected, err
}

// promptCloseFullyMerged confirms tearing down a fully-merged session
// at exit time. The branch and worktree are passed for the title,
// gitStatusVerbose is the (likely empty / "nothing to commit, working
// tree clean") output of `git status` so the user can see exactly what
// the worktree state is before agreeing. Default is affirmative — Enter
// closes.
func promptCloseFullyMerged(branch, defaultBranch, gitStatusVerbose string) (bool, error) {
	proceed := true
	err := huh.NewConfirm().
		Title(fmt.Sprintf("Branch %q is fully merged into %s. Close session and clean up worktree?", branch, defaultBranch)).
		Description(gitStatusVerbose).
		Affirmative("Close").
		Negative("Keep").
		Value(&proceed).
		Run()
	return proceed, err
}
