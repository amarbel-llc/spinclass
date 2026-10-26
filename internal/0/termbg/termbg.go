// Package termbg settles lipgloss's dark-background guess before anything can
// ask the terminal (#398).
//
// bubbletea's init() calls lipgloss.HasDarkBackground(), which makes termenv
// write OSC 11 + CPR queries to the tty and wait up to ~5s for a reply, for
// every command, interactive or not. lipgloss memoizes the answer in a
// sync.Once, and SetHasDarkBackground consumes that Once, so setting the value
// first means the terminal is never queried.
//
// This only works if our init runs before bubbletea's. Go initializes the
// first (by import path) package whose imports are all initialized; this
// package imports only lipgloss, and its path sorts before
// github.com/charmbracelet/bubbletea, so it runs right after lipgloss and
// before bubbletea. Import it blank from main before anything that pulls in
// bubbletea; keep its path sorting before "github.com/".
package termbg

import (
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func init() {
	lipgloss.SetHasDarkBackground(DarkFromColorFgBg(os.Getenv("COLORFGBG")))
}

// DarkFromColorFgBg reports whether a COLORFGBG value ("fg;bg" or
// "fg;default;bg") names a dark background. Unset or unparseable values are
// dark, the common case; background colours 7 and 15 are light.
func DarkFromColorFgBg(v string) bool {
	if v == "" {
		return true
	}
	fields := strings.Split(v, ";")
	bg, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		return true
	}
	return bg != 7 && bg != 15
}
