package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"golang.org/x/term"

	"code.linenisgreat.com/purse-first/libs/dewey/pkgs/mesa"
	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sessionpick"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// listMode is the resolved rendering for a CLI `sc list` invocation.
type listMode int

const (
	// modePlain emits the mesa summary table in its plain (tab-separated,
	// unstyled) form. Selected for explicit --format tap/table and for any
	// non-TTY stdout so pipes and command substitution stay clean.
	modePlain listMode = iota
	// modePretty emits the mesa summary table styled. The interactive
	// default: unset --format on a TTY.
	modePretty
	// modeJSON emits the machine-clean ListRow array (runListResult's json
	// path), regardless of TTY.
	modeJSON
	// modeWatch runs the full-screen live-refreshing bubbletea table,
	// rendering each frame through mesa.
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
// dispatches. modeJSON reuses runListResult so its output stays
// byte-identical to the MCP tool's (which never goes through this switch);
// modePretty, modePlain, and modeWatch render through mesa (RFC 0003) — the
// same List-Table renderer `clown list` migrated to (purse-first#185).
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
		fmt.Print(renderListTable(states, remoteRows, diags, closed, terminalWidth(), mesa.ForceStyle()))
		return nil
	case modeJSON:
		// FormatOrDefault() yields "json" exactly when --format json was
		// passed, matching the dispatch above. runListResult folds index
		// failures into res.Text, so fmt.Println(res.Text) reproduces the
		// framework's printResult exactly — keeping CLI output
		// byte-identical to the MCP tool's session.ListRow array.
		res, _ := runListResult(ctx, closed, g.FormatOrDefault(), dbg, remotes)
		fmt.Println(res.Text)
		return nil
	default: // modePlain
		states, remoteRows, diags, err := gatherListData(ctx, dbg, remotes)
		if err != nil {
			return err
		}
		fmt.Print(renderListTable(states, remoteRows, diags, closed, 0, mesa.ForcePlain()))
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
	// subtleColor dims the diagnostic lines and other secondary text (outside
	// the mesa table itself, which owns its own palette) while staying legible
	// on both light and dark terminals. A fixed ANSI "8" (bright black) renders
	// near-invisible against a dark background — looking "all black" next to
	// the default-foreground main text — so use an adaptive 256-color gray
	// that tracks the detected background the way the uncolored main text
	// already does.
	subtleColor = lipgloss.AdaptiveColor{Light: "240", Dark: "245"}

	dimStyle   = lipgloss.NewStyle().Foreground(subtleColor)
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

// stateSeverity maps a resolved session state to mesa's severity vocabulary,
// carrying the STATUS dot's color — see stateLegendEntries for the key.
// Unrecognized states (there should be none) fall back to Muted rather than
// Neutral so an unknown state still reads as "not clearly live".
func stateSeverity(state string) mesa.Severity {
	switch state {
	case session.StateActive:
		return mesa.OK
	case session.StateRunningDetached:
		return mesa.Accent
	case session.StateInactive:
		return mesa.Warn
	case session.StateAbandoned:
		return mesa.Muted
	default:
		return mesa.Muted
	}
}

// stateLegendEntries is the STATUS dot color key, rendered once in the
// table's footer by mesa's styled renderer so the dot-only column stays
// self-explanatory.
func stateLegendEntries() []mesa.LegendEntry {
	return []mesa.LegendEntry{
		mesa.Entry(mesa.OK, "●", "active"),
		mesa.Entry(mesa.Accent, "●", "running-detached"),
		mesa.Entry(mesa.Warn, "●", "inactive"),
		mesa.Entry(mesa.Muted, "●", "abandoned"),
	}
}

// clownBadge renders the STATUS badge for n live clowns under a session: one
// 🤡 per clown, so a busy session reads as "active, 🤡🤡🤡" — the badge width
// itself shows the count at a glance. Empty for zero.
func clownBadge(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("🤡", n)
}

// statusCell renders the merged STATUS cell: a severity-colored ● dot (see
// stateSeverity/stateLegendEntries), the clown-count badge, and an optional
// dim marker suffix (main / tombstone / dangling / remote).
func statusCell(sev mesa.Severity, clowns int, marker string) mesa.Cell {
	if marker != "" {
		marker = "(" + marker + ")"
	}
	return mesa.Status(sev, "", mesa.WithBadge(clownBadge(clowns)), mesa.WithMarker(marker))
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
// as a dim (Muted) trailing span (mirrors the text path's
// `spawned-by:<key>` column).
func descCell(desc, spawnedBy string) mesa.Cell {
	if spawnedBy == "" {
		return mesa.Text(desc)
	}
	hint := "↰" + spawnedBy
	if desc == "" {
		return mesa.Styled(mesa.Muted, hint)
	}
	return mesa.Spans(
		mesa.Span{Text: desc},
		mesa.Span{Text: " "},
		mesa.Span{Text: hint, Sev: mesa.Muted},
	)
}

func renderDiags(diags []string) string {
	lines := make([]string, len(diags))
	for i, d := range diags {
		lines[i] = dimStyle.Render(d)
	}
	return strings.Join(lines, "\n")
}

// renderListTable renders the session table — the summary view shared by the
// static pretty/plain paths and the --watch model — through mesa (RFC 0003;
// purse-first#185). Local states are sorted active-first (session.SortStates)
// and filtered the same way the text path is: abandoned/closed entries are
// dropped unless closed is true. The full worktree path is intentionally
// omitted (available via --format tap/json); this is a scannable summary.
// width is stdout's column count (0 ⇒ unknown, mesa sizes to content); opts
// selects styled vs plain rendering (mesa.ForceStyle / mesa.ForcePlain) —
// both modePretty and modePlain always resolve one of the two explicitly, so
// mesa's own TTY auto-detection is never relied on here. Pure: returns the
// rendered string.
func renderListTable(states []session.State, remoteRows []session.ListRow, diags []string, closed bool, width int, opts ...mesa.RenderOpt) string {
	now := time.Now()
	session.SortStates(states)

	// Clown presence (RFC-0014 §4): the 1-to-many "clowns under this session"
	// view, grouped by decoration == the spinclass session key. The count folds
	// into the STATUS cell — presence of live clowns IS activity — so there is
	// no separate CLOWNS column.
	presByKey := clown.PresenceByDecoration(now)

	t := mesa.New().
		Col("ID", mesa.Pin).
		Col("STATUS", mesa.Pin).
		Col("AGE", mesa.Pin).
		Col("DESCRIPTION", mesa.Flex).
		Legend(stateLegendEntries()...).
		Empty("No sessions.")

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
		// Status uses the presence-aware display state so a live clown over a dead
		// spinclass PID reads running-detached, not inactive (#153) — never
		// contradicting the 🤡 count beside it. Filter/marker stay on the base
		// resolved state above so presence never un-abandons a row.
		t.Row(
			mesa.Text(s.SessionKey),
			statusCell(stateSeverity(s.ResolveDisplayState(clowns)), clowns, marker),
			mesa.Text(sessionpick.FormatRelDate(sessionpick.LastActivity(*s), now)),
			descCell(s.Description, s.SpawnedBy),
		)
	}
	for _, r := range remoteRows {
		// Combine the host and the repo/id session key into the single ID cell,
		// styled Special (magenta) so remote rows still stand out — as is the
		// STATUS dot, uniformly, regardless of the remote's reported state.
		// Host-first mirrors the `host:` resume-target convention (FDR 0011):
		// <remote>:<repo>/<id>.
		t.Row(
			mesa.Styled(mesa.Special, r.Remote+":"+r.Repo+"/"+r.ID),
			statusCell(mesa.Special, r.ClownCount, "remote"),
			mesa.Text(""),
			descCell(r.Description, r.SpawnedBy),
		)
	}

	var b strings.Builder
	if width > 0 {
		opts = append(opts, mesa.Width(width))
	}
	if err := t.Render(&b, opts...); err != nil {
		return "render error: " + err.Error() + "\n"
	}
	out := b.String()
	if len(diags) > 0 {
		out += renderDiags(diags) + "\n"
	}
	return out
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
	b.WriteString(renderListTable(m.states, m.remoteRows, m.diags, m.closed, m.width, mesa.ForceStyle()))
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
