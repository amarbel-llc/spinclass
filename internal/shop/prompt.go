package shop

import (
	"fmt"

	"github.com/charmbracelet/huh"
)

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
