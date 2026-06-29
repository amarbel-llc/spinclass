package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mattn/go-isatty"
	"golang.org/x/term"

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
		fmt.Print(renderListTable(states, remoteRows, diags, closed, terminalWidth()))
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
	// subtleColor dims the borders, the dot legend, and other secondary text
	// while staying legible on both light and dark terminals. A fixed ANSI "8"
	// (bright black) renders near-invisible against a dark background — looking
	// "all black" next to the default-foreground main text — so use an adaptive
	// 256-color gray that tracks the detected background the way the uncolored
	// main text already does.
	subtleColor = lipgloss.AdaptiveColor{Light: "240", Dark: "245"}

	// stateStyles colors the STATUS dot by resolved session state. ANSI palette
	// indices keep the live states readable across terminal themes; abandoned
	// reuses the adaptive subtle gray so it never disappears into the bg.
	stateStyles = map[string]lipgloss.Style{
		session.StateActive:          lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true), // green
		session.StateRunningDetached: lipgloss.NewStyle().Foreground(lipgloss.Color("6")),            // cyan
		session.StateInactive:        lipgloss.NewStyle().Foreground(lipgloss.Color("3")),            // yellow
		session.StateAbandoned:       lipgloss.NewStyle().Foreground(subtleColor),                    // gray
	}
	dimStyle           = lipgloss.NewStyle().Foreground(subtleColor)
	headerStyle        = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	cellStyle          = lipgloss.NewStyle().Padding(0, 1)
	borderStyle        = lipgloss.NewStyle().Foreground(subtleColor)
	remoteSessionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5")) // magenta
	titleStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	errStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

// statusCell renders the merged STATUS cell: a state-colored ● dot, one 🤡 per
// live clown running under the session (so a busy session reads as "active,
// 🤡🤡🤡" — the badge width itself shows the count at a glance), and an optional
// dim marker suffix (main / tombstone / dangling / remote). State is carried by
// the dot's color alone — see statusLegend for the key — which the dot+clowns
// design trades word-legibility for a far narrower column.
func statusCell(state string, clowns int, marker string) string {
	st, ok := stateStyles[state]
	if !ok {
		st = dimStyle
	}
	out := st.Render("●")
	if clowns > 0 {
		out += " " + st.Render(strings.Repeat("🤡", clowns))
	}
	if marker != "" {
		out += " " + dimStyle.Render("("+marker+")")
	}
	return out
}

// statusLegend is the dim key decoding the STATUS dot colors, rendered once in
// the table's footer row (legendFooter) so the dot-only column stays
// self-explanatory.
func statusLegend() string {
	dot := func(state, label string) string {
		return stateStyles[state].Render("●") + dimStyle.Render(" "+label)
	}
	return strings.Join([]string{
		dot(session.StateActive, "active"),
		dot(session.StateRunningDetached, "running-detached"),
		dot(session.StateInactive, "inactive"),
		dot(session.StateAbandoned, "abandoned"),
	}, dimStyle.Render(" · "))
}

// terminalWidth reports stdout's column count for description wrapping, or 0
// when stdout is not a sized terminal (callers treat 0 as "don't wrap").
func terminalWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 0
	}
	return w
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

// descColumn is the 0-based index of the DESCRIPTION column in the table
// layout below (REPO · NAME · STATUS · AGE · DESCRIPTION). It is the only
// column given a fixed width, so it is the one that wraps.
const descColumn = 4

// listTableRow is one fully-rendered row of the summary table.
type listTableRow struct{ repo, name, status, age, desc string }

// repoAndName splits a session key (<repo>/<rest>, where <rest> is a branch or
// the implicit-session random id) into its repo and name halves for the first
// two columns — eliminating the old SESSION+BRANCH redundancy where the branch
// was printed both inside the key and again on its own. Cutting on the first
// "/" keeps slashes in branch names (e.g. feature/foo) attached to the name.
func repoAndName(sessionKey string) (repo, name string) {
	repo, name, found := strings.Cut(sessionKey, "/")
	if !found {
		return "", sessionKey
	}
	return repo, name
}

// renderListTable renders the styled session table — the summary view shared
// by the static pretty path and the --watch model. Local states are sorted
// active-first (session.SortStates) and filtered the same way the text path
// is: abandoned/closed entries are dropped unless closed is true. The full
// worktree path is intentionally omitted (available via --format tap/json);
// this is a scannable summary. width is stdout's column count (0 ⇒ unknown);
// when known, the DESCRIPTION column is bounded so long descriptions wrap
// inside their cell instead of overflowing and breaking the grid. Pure:
// returns the rendered string with a trailing newline.
func renderListTable(states []session.State, remoteRows []session.ListRow, diags []string, closed bool, width int) string {
	now := time.Now()
	session.SortStates(states)

	// Clown presence (RFC-0014 §4): the 1-to-many "clowns under this session"
	// view, grouped by decoration == the spinclass session key. The count folds
	// into the STATUS cell — presence of live clowns IS activity — so there is
	// no longer a separate CLOWNS column.
	presByKey := clown.PresenceByDecoration(now)

	var rows []listTableRow

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
		repo, name := repoAndName(s.SessionKey)
		// Status uses the presence-aware display state so a live clown over a dead
		// spinclass PID reads running-detached, not inactive (#153) — never
		// contradicting the 🤡 count beside it. Filter/marker stay on the base
		// resolved state above so presence never un-abandons a row.
		rows = append(rows, listTableRow{
			repo:   repo,
			name:   name,
			status: statusCell(s.ResolveDisplayState(clowns), clowns, marker),
			age:    sessionpick.FormatRelDate(sessionpick.LastActivity(*s), now),
			desc:   descCell(s.Description, s.SpawnedBy),
		})
	}
	for _, r := range remoteRows {
		rows = append(rows, listTableRow{
			repo:   r.Repo,
			name:   remoteSessionStyle.Render(r.Remote + ":" + r.ID),
			status: statusCell(r.State, r.ClownCount, "remote"),
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

	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		BorderBottom(false). // the legend footer (legendFooter) supplies the closing border
		Headers("REPO", "NAME", "STATUS", "AGE", "DESCRIPTION").
		StyleFunc(listTableStyleFunc(width, fixedColumnWidths(rows)))
	// Description-column wrapping: when stdout's width is known, give
	// the table that total width and pin the four narrow columns to their content
	// width (below); lipgloss's resizer grows the shortest column and shrinks the
	// biggest but skips any column already at its fixed width, so it flexes only
	// the one unpinned column — DESCRIPTION — to the remaining space, wrapping it
	// (table wrapping is on by default) rather than letting it overflow and break
	// the grid. Width 0 (non-TTY / unknown) keeps the legacy content-sized layout.
	if width > 0 {
		t = t.Width(width)
	}
	for _, r := range rows {
		t.Row(r.repo, r.name, r.status, r.age, r.desc)
	}
	out := legendFooter(t.Render(), statusLegend())
	if len(diags) > 0 {
		out += "\n" + renderDiags(diags)
	}
	return out + "\n"
}

// ansiSGR matches SGR color/style escape sequences, used to measure the
// table's border geometry from its (styled) top border line.
var ansiSGR = regexp.MustCompile("\x1b\\[[0-9;]*m")

// legendFooter attaches the status-dot color key as a footer row spanning the
// full width of body — a table rendered WITHOUT its bottom border. It reads the
// column geometry from the table's top border line (stripped of color) so the
// connecting rule can drop a ┴ under each column boundary, merging the columns
// into the spanning footer; the legend is then framed to the table's inner
// width (lipgloss wraps it if the terminal is narrower than the key) and closed
// by a flat bottom border. Border glyphs reuse borderStyle so the footer is the
// same color as the rest of the grid.
func legendFooter(body, legend string) string {
	lines := strings.Split(body, "\n")
	top := []rune(ansiSGR.ReplaceAllString(lines[0], ""))
	total := len(top)
	if total < 2 {
		// Degenerate (no recognizable border) — fall back to a plain trailing line.
		return body + "\n" + legend
	}
	inner := total - 2

	connector := make([]rune, total)
	bottom := make([]rune, total)
	for i := range connector {
		connector[i], bottom[i] = '─', '─'
	}
	connector[0], connector[total-1] = '├', '┤'
	bottom[0], bottom[total-1] = '╰', '╯'
	for i, r := range top {
		if r == '┬' {
			connector[i] = '┴'
		}
	}

	bar := borderStyle.Render("│")
	keyBody := lipgloss.NewStyle().Width(inner).Padding(0, 1).Align(lipgloss.Center).Render(legend)
	out := append(lines, borderStyle.Render(string(connector)))
	for _, ln := range strings.Split(keyBody, "\n") {
		out = append(out, bar+ln+bar)
	}
	out = append(out, borderStyle.Render(string(bottom)))
	return strings.Join(out, "\n")
}

// fixedColumnWidths returns the content width to pin each non-DESCRIPTION column
// to: the widest cell in that column, headers included. Pinning these lets
// lipgloss direct all of the table's flex onto the single unpinned DESCRIPTION
// column (see renderListTable).
func fixedColumnWidths(rows []listTableRow) [descColumn]int {
	w := [descColumn]int{
		lipgloss.Width("REPO"),
		lipgloss.Width("NAME"),
		lipgloss.Width("STATUS"),
		lipgloss.Width("AGE"),
	}
	for _, r := range rows {
		w[0] = max(w[0], lipgloss.Width(r.repo))
		w[1] = max(w[1], lipgloss.Width(r.name))
		w[2] = max(w[2], lipgloss.Width(r.status))
		w[3] = max(w[3], lipgloss.Width(r.age))
	}
	return w
}

// listTableStyleFunc returns the per-cell StyleFunc. Headers get headerStyle;
// when width is known the four leading columns are pinned to their content
// width (fixed[c]) so DESCRIPTION is the only column lipgloss reflows. With
// width 0 every body cell gets the plain padded cellStyle and the table sizes
// to content.
func listTableStyleFunc(width int, fixed [descColumn]int) table.StyleFunc {
	return func(r, c int) lipgloss.Style {
		if r == table.HeaderRow {
			return headerStyle
		}
		if width > 0 && c < descColumn {
			// lipgloss .Width() is the total cell width including padding, so add
			// cellStyle's horizontal padding back onto the measured content width —
			// otherwise the pinned column truncates its content by the padding.
			return cellStyle.Width(fixed[c] + cellStyle.GetHorizontalPadding())
		}
		return cellStyle
	}
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
	width       int // terminal columns, from the latest WindowSizeMsg (0 until first)
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
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
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
	b.WriteString(renderListTable(m.states, m.remoteRows, m.diags, m.closed, m.width))
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
