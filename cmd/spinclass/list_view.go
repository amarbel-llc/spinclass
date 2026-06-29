package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mattn/go-isatty"

	"github.com/amarbel-llc/spinclass/internal/clown"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sessionpick"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

// listMode is the resolved rendering for a CLI `sc list` invocation.
type listMode int

const (
	// modePlain emits the legacy tab-separated text (runListResult's text
	// path). Selected for explicit --format tap/table and for any non-TTY
	// stdout so pipes and command substitution stay clean.
	modePlain listMode = iota
	// modePretty emits the styled lipgloss table. The interactive default:
	// unset --format on a TTY.
	modePretty
	// modeJSON emits the machine-clean ListRow array (runListResult's json
	// path), regardless of TTY.
	modeJSON
	// modeWatch runs the full-screen live-refreshing bubbletea table.
	modeWatch
)

// listRenderMode resolves how the CLI should render `sc list`. It reads the
// raw --format string (NOT FormatOrDefault, which collapses unset and an
// explicit "tap" to the same value — losing the distinction that makes a
// bare `sc list` on a TTY pretty while `sc list --format tap` stays plain).
func listRenderMode(format string, isTTY, watch bool) listMode {
	switch {
	case watch:
		return modeWatch
	case format == "json":
		return modeJSON
	case format == "" && isTTY:
		return modePretty
	default:
		return modePlain
	}
}

// runListCLI is the `sc list` RunCLI body: it resolves the render mode and
// dispatches. The plain/json branches reuse runListResult so their output
// stays byte-identical to the MCP tool's; pretty and watch are the new
// charm paths and never run for the MCP tool (which has no RunCLI).
func runListCLI(ctx context.Context, g globalArgs, closed, watch bool, interval string, remotes []sweatfile.Remote) error {
	dbg := g.debugLogger()
	isTTY := isatty.IsTerminal(os.Stdout.Fd())

	switch listRenderMode(g.Format, isTTY, watch) {
	case modeWatch:
		d, err := parseWatchInterval(interval)
		if err != nil {
			return err
		}
		return runListWatch(ctx, closed, d, dbg, remotes)
	case modePretty:
		states, remoteRows, diags, err := gatherListData(ctx, dbg, remotes)
		if err != nil {
			return err
		}
		fmt.Print(renderListTable(states, remoteRows, diags, closed))
		return nil
	default: // modePlain, modeJSON
		// Both reuse runListResult. FormatOrDefault() already yields "json"
		// exactly when --format json was passed (and "tap" otherwise), so one
		// branch covers both modes. runListResult folds index failures into
		// res.Text, so fmt.Println(res.Text) reproduces the framework's
		// printResult exactly — keeping CLI output byte-identical to the MCP tool.
		res, _ := runListResult(ctx, closed, g.FormatOrDefault(), dbg, remotes)
		fmt.Println(res.Text)
		return nil
	}
}

// parseWatchInterval resolves the --interval flag: empty → 2s default, an
// unparseable or non-positive duration → error (so a typo fails loudly
// rather than busy-looping or never ticking).
func parseWatchInterval(s string) (time.Duration, error) {
	if s == "" {
		return 2 * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --interval %q: %w", s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("--interval must be positive, got %q", s)
	}
	return d, nil
}

// ---- styling ---------------------------------------------------------------

var (
	// stateStyles colors the STATE cell by resolved session state. ANSI
	// palette indices keep it readable across terminal themes.
	stateStyles = map[string]lipgloss.Style{
		session.StateActive:          lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true), // green
		session.StateRunningDetached: lipgloss.NewStyle().Foreground(lipgloss.Color("6")),            // cyan
		session.StateInactive:        lipgloss.NewStyle().Foreground(lipgloss.Color("3")),            // yellow
		session.StateAbandoned:       lipgloss.NewStyle().Foreground(lipgloss.Color("8")),            // gray
	}
	dimStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	headerStyle        = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	cellStyle          = lipgloss.NewStyle().Padding(0, 1)
	borderStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	remoteSessionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5")) // magenta
	titleStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	errStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

// stateCell renders the colored STATE cell with an optional dim marker
// suffix (main / tombstone / dangling / remote).
func stateCell(state, marker string) string {
	st, ok := stateStyles[state]
	if !ok {
		st = dimStyle
	}
	out := st.Render(state)
	if marker != "" {
		out += " " + dimStyle.Render("("+marker+")")
	}
	return out
}

// descCell renders the DESCRIPTION cell, folding the spawn-lineage hint in
// as a dim suffix (mirrors the text path's `spawned-by:<key>` column).
func descCell(desc, spawnedBy string) string {
	if spawnedBy == "" {
		return desc
	}
	hint := dimStyle.Render("↰" + spawnedBy)
	if desc == "" {
		return hint
	}
	return desc + " " + hint
}

func renderDiags(diags []string) string {
	lines := make([]string, len(diags))
	for i, d := range diags {
		lines[i] = dimStyle.Render(d)
	}
	return strings.Join(lines, "\n")
}

// clownsCell renders the count of live clowns running under a session (from
// clown's presence index), or "" when none — so the column reads clean for
// sessions with no attached clown.
func clownsCell(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// renderListTable renders the styled session table — the summary view shared
// by the static pretty path and the --watch model. Local states are sorted
// active-first (session.SortStates) and filtered the same way the text path
// is: abandoned/closed entries are dropped unless closed is true. The full
// worktree path is intentionally omitted (available via --format tap/json);
// this is a scannable summary. Pure: returns the rendered string with a
// trailing newline.
func renderListTable(states []session.State, remoteRows []session.ListRow, diags []string, closed bool) string {
	now := time.Now()
	session.SortStates(states)

	// Clown presence (RFC-0014 §4): the 1-to-many "clowns under this session"
	// view, grouped by decoration == the spinclass session key. The CLOWNS
	// column is shown only when some clown presence exists, so a bare spinclass
	// (or no clown) keeps the legacy 5-column table.
	presByKey := clown.PresenceByDecoration(now)
	showClowns := len(presByKey) > 0

	type row struct{ state, sess, branch, age, desc, clowns string }
	var rows []row

	for i := range states {
		s := &states[i]
		clowns := len(presByKey[s.SessionKey])
		resolved := s.ResolveState()
		isClosed := resolved == session.StateAbandoned
		if isClosed && !closed {
			continue
		}
		marker := ""
		switch {
		case s.IsTombstone():
			marker = "tombstone"
		case isClosed:
			marker = "dangling"
		case s.Kind == session.KindImplicit:
			marker = "main"
		}
		// State cell uses the presence-aware display state so a live clown over a
		// dead spinclass PID reads running-detached, not inactive (#153) — never
		// contradicting the CLOWNS count beside it. Filter/marker stay on the base
		// resolved state above so presence never un-abandons a row.
		rows = append(rows, row{
			state:  stateCell(s.ResolveDisplayState(clowns), marker),
			sess:   s.SessionKey,
			branch: s.Branch,
			age:    sessionpick.FormatRelDate(sessionpick.LastActivity(*s), now),
			desc:   descCell(s.Description, s.SpawnedBy),
			clowns: clownsCell(clowns),
		})
	}
	for _, r := range remoteRows {
		rows = append(rows, row{
			state:  stateCell(r.State, "remote"),
			sess:   remoteSessionStyle.Render(r.Remote + ":" + r.ID),
			branch: r.Branch,
			age:    "",
			desc:   descCell(r.Description, r.SpawnedBy),
		})
	}

	if len(rows) == 0 {
		out := dimStyle.Render("No sessions.")
		if len(diags) > 0 {
			out += "\n" + renderDiags(diags)
		}
		return out + "\n"
	}

	headers := []string{"STATE"}
	if showClowns {
		headers = append(headers, "CLOWNS")
	}
	headers = append(headers, "SESSION", "BRANCH", "AGE", "DESCRIPTION")

	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		Headers(headers...).
		StyleFunc(func(r, _ int) lipgloss.Style {
			if r == table.HeaderRow {
				return headerStyle
			}
			return cellStyle
		})
	for _, r := range rows {
		cells := []string{r.state}
		if showClowns {
			cells = append(cells, r.clowns)
		}
		cells = append(cells, r.sess, r.branch, r.age, r.desc)
		t.Row(cells...)
	}
	out := t.Render()
	if len(diags) > 0 {
		out += "\n" + renderDiags(diags)
	}
	return out + "\n"
}

// ---- watch model -----------------------------------------------------------

// listTickMsg fires on the refresh ticker; listReloadMsg carries a fresh
// local-index read. Remote rows are NOT re-queried per tick (local-only live
// refresh — FDR fast-follow for per-tick remotes); they stay as fetched at
// startup.
type (
	listTickMsg   time.Time
	listReloadMsg struct {
		states []session.State
		err    error
	}
)

// watchModel is the bubbletea model behind `sc list --watch`. It mirrors
// sessionpick.pickerModel's thin shape: reload + tick commands, quit on
// q/esc/ctrl+c, render via renderListTable.
type watchModel struct {
	closed      bool
	interval    time.Duration
	remoteRows  []session.ListRow // static: fetched once at startup
	diags       []string          // static: remote diagnostics from startup
	states      []session.State   // reloaded each tick
	loadErr     error
	lastRefresh time.Time
}

func reloadListCmd() tea.Msg {
	states, err := session.ListAll(nil)
	return listReloadMsg{states: states, err: err}
}

func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return listTickMsg(t) })
}

func (m watchModel) Init() tea.Cmd {
	return tea.Batch(reloadListCmd, tickCmd(m.interval))
}

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "r":
			return m, reloadListCmd // manual refresh
		}
	case listTickMsg:
		return m, tea.Batch(reloadListCmd, tickCmd(m.interval))
	case listReloadMsg:
		m.states = msg.states
		m.loadErr = msg.err
		m.lastRefresh = time.Now()
		return m, nil
	}
	return m, nil
}

func (m watchModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("spinclass sessions"))
	if !m.lastRefresh.IsZero() {
		b.WriteString(dimStyle.Render("  updated " + m.lastRefresh.Format("15:04:05")))
	}
	b.WriteString("\n")
	if m.loadErr != nil {
		b.WriteString(errStyle.Render("reload error: "+m.loadErr.Error()) + "\n")
	}
	b.WriteString(renderListTable(m.states, m.remoteRows, m.diags, m.closed))
	b.WriteString(dimStyle.Render(fmt.Sprintf("q quit · r refresh · every %s", m.interval)))
	return b.String()
}

// runListWatch fetches remotes once, seeds the model with the initial local
// read (so the first frame is populated), and runs the alt-screen program.
// --watch is a TTY-only affordance: a non-TTY stdout returns a clean error
// rather than launching a program that would render escape sequences into a
// pipe (mirrors sessionpick's non-TTY guard).
func runListWatch(ctx context.Context, closed bool, interval time.Duration, dbg *slog.Logger, remotes []sweatfile.Remote) error {
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("--watch requires an interactive terminal (stdout is not a TTY)")
	}
	states, remoteRows, diags, err := gatherListData(ctx, dbg, remotes)
	if err != nil {
		return err
	}
	m := watchModel{
		closed:      closed,
		interval:    interval,
		remoteRows:  remoteRows,
		diags:       diags,
		states:      states,
		lastRefresh: time.Now(),
	}
	_, err = tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	return err
}
