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
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"

	"code.linenisgreat.com/spinclass/internal/session"
)

// Item is one selectable row in the picker. Local session rows are built
// from session.State via ItemForState; cached remote rows via
// ItemForRemoteRow (State stays nil for those — selection yields Target).
type Item struct {
	// TitleText is the row title: the session description, falling back
	// to the worktree dir name (local) or the remote id (remote).
	TitleText string
	// Detail is the row description line rendered under the title.
	Detail string
	// Filter is the haystack the list's fuzzy filter matches against.
	Filter string
	// State is the local session backing this row; nil for remote rows.
	State *session.State
	// Target is the `host:<id>` resume target for remote rows; empty for
	// local rows (State carries everything the caller needs there).
	Target string
}

func (i Item) Title() string       { return i.TitleText }
func (i Item) Description() string { return i.Detail }
func (i Item) FilterValue() string { return i.Filter }

// ItemForState builds the picker row for a local session. now is
// injected for testability of the relative-time tiering. Exported so
// tab completion can reuse Detail and read identically to the picker.
func ItemForState(s session.State, now time.Time) Item {
	title := s.Description
	if title == "" {
		title = filepath.Base(s.WorktreePath)
	}
	repoBase := filepath.Base(s.RepoPath)
	detail := strings.Join([]string{
		s.ResolveState(),
		FormatRelDate(LastActivity(s), now),
		"@" + s.Branch,
		repoBase,
	}, " · ")
	return Item{
		TitleText: title,
		Detail:    detail,
		Filter:    title + " " + filepath.Base(s.WorktreePath) + " " + repoBase,
	}
}

// ItemForRemoteRow builds the picker row for a cached remote session
// (the FDR 0011 completion cache — possibly stale, never networks,
// hence the "cached" marker). State stays nil; selection yields the
// `host:<id>` Target, which the caller routes over the remote's attach
// template.
func ItemForRemoteRow(name string, r session.ListRow) Item {
	title := r.Description
	if title == "" {
		title = r.ID
	}
	target := name + ":" + r.ID
	return Item{
		TitleText: title,
		Detail:    fmt.Sprintf("remote(%s) · %s · cached", name, r.State),
		Filter:    title + " " + target + " " + r.Repo,
		Target:    target,
	}
}

// ItemsForRemoteRows flattens the per-remote cache map (the shape
// remote.ReadAllCaches returns) into picker rows: hosts sorted by name
// for deterministic order, rows kept in cache order within a host.
func ItemsForRemoteRows(caches map[string][]session.ListRow) []Item {
	names := make([]string, 0, len(caches))
	for name := range caches {
		names = append(names, name)
	}
	sort.Strings(names)
	var items []Item
	for _, name := range names {
		for _, r := range caches[name] {
			items = append(items, ItemForRemoteRow(name, r))
		}
	}
	return items
}

// LastActivity returns the session's most recent lifecycle timestamp:
// ExitedAt when set, else StartedAt. Shared by the picker rows and the
// resume confirm dialog's detail block.
func LastActivity(s session.State) time.Time {
	if s.ExitedAt != nil && !s.ExitedAt.IsZero() {
		return *s.ExitedAt
	}
	return s.StartedAt
}

// FormatRelDate renders t relative to now using clown's tiering:
// just now / Nm ago / Nh ago / Nd ago / absolute date beyond a week.
func FormatRelDate(t, now time.Time) string {
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
// (nil, nil) — the caller treats it as "user cancelled" and exits 0.
func Choose(repoPath, cmdName string, dbg *slog.Logger) (*session.State, error) {
	item, _, err := choose(repoPath, cmdName, dbg, false, nil)
	if err != nil || item == nil {
		return nil, err
	}
	return item.State, nil
}

// ChooseAutoSingle is Choose with a single-candidate short-circuit:
// when exactly one non-abandoned LOCAL session exists for repoPath it
// is returned without rendering the picker (auto=true), even on a
// non-TTY stdin — the caller owns whatever confirmation that path
// needs. With multiple candidates it behaves exactly like Choose
// (auto=false). remoteRows (built via ItemsForRemoteRows) are appended
// after the local rows in the picker; they never count toward the
// single-match shortcut — a remote row is only ever reachable by
// explicit selection, which yields an Item with nil State and its
// `host:<id>` Target set. `resume` uses this so the single-match case
// shows a confirm dialog instead of a one-row picker; `close` keeps
// Choose so its behavior stays local-only.
func ChooseAutoSingle(repoPath, cmdName string, dbg *slog.Logger, remoteRows []Item) (*Item, bool, error) {
	return choose(repoPath, cmdName, dbg, true, remoteRows)
}

func choose(repoPath, cmdName string, dbg *slog.Logger, autoSingle bool, remoteRows []Item) (*Item, bool, error) {
	sessions, err := session.ListForRepo(repoPath, dbg)
	if err != nil {
		return nil, false, err
	}
	if len(sessions) == 0 && len(remoteRows) == 0 {
		return nil, false, fmt.Errorf("no sessions for %s", filepath.Base(repoPath))
	}
	session.SortStates(sessions)

	// The shortcut counts LOCAL candidates only: a lone local session
	// auto-resolves even when cached remote rows exist, and remote-only
	// candidates always reach the picker.
	if autoSingle && len(sessions) == 1 {
		it := ItemForState(sessions[0], time.Now())
		it.State = &sessions[0]
		return &it, true, nil
	}

	if !interactive() {
		ids := make([]string, 0, len(sessions)+len(remoteRows))
		for _, s := range sessions {
			ids = append(ids, filepath.Base(s.WorktreePath))
		}
		for _, r := range remoteRows {
			ids = append(ids, r.Target)
		}
		return nil, false, fmt.Errorf(
			"no session selected; available: %s\nUse: spinclass %s <id>",
			strings.Join(ids, ", "),
			cmdName,
		)
	}

	now := time.Now()
	items := make([]Item, 0, len(sessions)+len(remoteRows))
	for i := range sessions {
		it := ItemForState(sessions[i], now)
		it.State = &sessions[i]
		items = append(items, it)
	}
	items = append(items, remoteRows...)

	picked, err := Pick(fmt.Sprintf("Select a session to %s", cmdName), items)
	if err != nil {
		return nil, false, fmt.Errorf("session picker: %w", err)
	}
	if picked == nil {
		// User dismissed the picker (q/esc/ctrl+c): nil item, nil error —
		// callers exit 0 without acting, clown-style.
		return nil, false, nil
	}
	return picked, false, nil
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
