// Package sessionpick wraps the interactive session picker shared by
// `sc resume` and `sc close`. Both commands need the same shape:
// list-active-sessions-for-repo, sort, render a huh menu when stdin is
// a TTY, return a list-of-IDs error when it isn't.
package sessionpick

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"

	"github.com/amarbel-llc/spinclass/internal/session"
)

// Choose returns the session the user picked from the list of
// non-abandoned sessions for repoPath. cmdName is only used to build a
// helpful "Use: spinclass <cmdName> <id>" hint when stdin isn't a TTY.
// dbg, when non-nil, receives Debug-level records describing every index
// entry that was excluded by session.ListAll/ListForRepo — pass nil for
// silent operation (e.g. tab-completion paths).
func Choose(repoPath, cmdName string, dbg *slog.Logger) (*session.State, error) {
	sessions, err := session.ListForRepo(repoPath, dbg)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("no sessions for %s", filepath.Base(repoPath))
	}
	session.SortStates(sessions)

	if !interactive() {
		ids := make([]string, len(sessions))
		for i, s := range sessions {
			ids[i] = filepath.Base(s.WorktreePath)
		}
		return nil, fmt.Errorf(
			"no session selected; available: %s\nUse: spinclass %s <id>",
			strings.Join(ids, ", "),
			cmdName,
		)
	}

	options := make([]huh.Option[int], len(sessions))
	for i, s := range sessions {
		options[i] = huh.NewOption(label(s), i)
	}

	var selected int
	if err := huh.NewSelect[int]().
		Title(fmt.Sprintf("Select session to %s", cmdName)).
		Options(options...).
		Value(&selected).
		Run(); err != nil {
		return nil, fmt.Errorf("session selection cancelled")
	}
	return &sessions[selected], nil
}

func interactive() bool {
	fd := os.Stdin.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

func label(s session.State) string {
	state := s.ResolveState()
	if s.Description != "" {
		return fmt.Sprintf("%s [%s] — %s", s.Branch, state, s.Description)
	}
	return fmt.Sprintf("%s [%s]", s.Branch, state)
}
