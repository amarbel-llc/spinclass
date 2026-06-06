// Package sessionpick wraps the interactive session picker shared by
// `sc resume` and `sc close`. Both commands need the same shape:
// list-active-sessions-for-repo, sort, render a clown-style filterable
// bubbletea list picker when stdin is a TTY, return a list-of-IDs error
// when it isn't. Modeled on clown's `clown resume` picker (see
// docs/plans/2026-06-06-clown-style-resume-design.md).
package sessionpick

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"

	"github.com/amarbel-llc/spinclass/internal/session"
)

// Item is one selectable row in the picker. Local session rows are built
// from session.State via itemForState; a later task adds remote rows by
// constructing Items directly (State stays nil for those) and passing
// them to Pick alongside the local rows.
type Item struct {
	// TitleText is the row title: the session description, falling back
	// to the worktree dir name.
	TitleText string
	// Detail is the row description line rendered under the title.
	Detail string
	// Filter is the haystack the list's fuzzy filter matches against.
	Filter string
	// State is the local session backing this row; nil for non-local
	// rows (future remote entries).
	State *session.State
}

func (i Item) Title() string       { return i.TitleText }
func (i Item) Description() string { return i.Detail }
func (i Item) FilterValue() string { return i.Filter }

// itemForState builds the picker row for a local session. now is
// injected for testability of the relative-time tiering.
func itemForState(s session.State, now time.Time) Item {
	title := s.Description
	if title == "" {
		title = filepath.Base(s.WorktreePath)
	}
	last := s.StartedAt
	if s.ExitedAt != nil && !s.ExitedAt.IsZero() {
		last = *s.ExitedAt
	}
	repoBase := filepath.Base(s.RepoPath)
	detail := strings.Join([]string{
		s.ResolveState(),
		formatRelDate(last, now),
		"@" + s.Branch,
		repoBase,
	}, " · ")
	return Item{
		TitleText: title,
		Detail:    detail,
		Filter:    title + " " + filepath.Base(s.WorktreePath) + " " + repoBase,
	}
}

// formatRelDate renders t relative to now using clown's tiering:
// just now / Nm ago / Nh ago / Nd ago / absolute date beyond a week.
func formatRelDate(t, now time.Time) string {
	delta := now.Sub(t)
	switch {
	case delta < time.Minute:
		return "just now"
	case delta < time.Hour:
		return fmt.Sprintf("%dm ago", int(delta.Minutes()))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(delta.Hours()))
	case delta < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(delta.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// Choose returns the session the user picked from the list of
// non-abandoned sessions for repoPath. cmdName names the verb in the
// picker title and builds a helpful "Use: spinclass <cmdName> <id>"
// hint when stdin isn't a TTY. dbg, when non-nil, receives Debug-level
// records describing every index entry that was excluded by
// session.ListAll/ListForRepo — pass nil for silent operation (e.g.
// tab-completion paths). Dismissing the picker (q/esc/ctrl+c) returns
// the historical "session selection cancelled" error.
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

	now := time.Now()
	items := make([]Item, len(sessions))
	for i := range sessions {
		items[i] = itemForState(sessions[i], now)
		items[i].State = &sessions[i]
	}

	picked, err := Pick(fmt.Sprintf("Select a session to %s", cmdName), items)
	if err != nil {
		return nil, fmt.Errorf("session picker: %w", err)
	}
	if picked == nil {
		return nil, fmt.Errorf("session selection cancelled")
	}
	return picked.State, nil
}

func interactive() bool {
	fd := os.Stdin.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// pickerModel is the thin bubbletea glue around the bubbles list,
// mirroring clown's sessionPickerModel.
type pickerModel struct {
	list   list.Model
	chosen *Item
	quit   bool
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// While the user is typing a filter, every key (including q,
		// esc, enter) belongs to the list's filter input.
		if m.list.FilterState() == list.Filtering {
			break
		}
		switch msg.String() {
		case "enter":
			if it, ok := m.list.SelectedItem().(Item); ok {
				m.chosen = &it
			}
			return m, tea.Quit
		case "q", "ctrl+c", "esc":
			m.quit = true
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.list.SetWidth(msg.Width)
		m.list.SetHeight(msg.Height - 2)
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m pickerModel) View() string { return m.list.View() }

// Pick runs the full-screen filterable picker over items and returns
// the chosen one, or (nil, nil) when the user dismissed it. Exported so
// a later task can pass extra (remote) rows without reworking this
// package.
func Pick(title string, items []Item) (*Item, error) {
	li := make([]list.Item, len(items))
	for i, it := range items {
		li[i] = it
	}
	l := list.New(li, list.NewDefaultDelegate(), 60, 16)
	l.Title = title
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	m, err := tea.NewProgram(pickerModel{list: l}, tea.WithAltScreen()).Run()
	if err != nil {
		return nil, err
	}
	pm := m.(pickerModel)
	if pm.quit {
		return nil, nil
	}
	return pm.chosen, nil
}
