package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/spinclass/internal/clean"
	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/pull"
	"code.linenisgreat.com/spinclass/internal/remote"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/shop"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/validate"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

func registerQueryCommands(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "list",
		Title: "List Spinclass Sessions",
		Description: command.Description{
			Short: "List tracked sessions",
			Long: "List tracked sessions from the central index. By default " +
				"only live entries (active or running-detached) are shown. " +
				"Use --closed to also include tombstones (cleanly-closed " +
				"sessions) and dangling symlinks (externally-closed). " +
				"Hosts declared via [[remotes]] in the sweatfile hierarchy " +
				"are queried in parallel and their sessions appended as " +
				"host:-prefixed rows; an unreachable host yields a per-host " +
				"diagnostic line in text formats, while --format json keeps " +
				"a machine-clean array and adds a \"remote\" field to " +
				"remote rows.\n\n" +
				"On an interactive terminal the CLI renders a styled table; " +
				"piped output, --format json, and --format tap/table keep the " +
				"plain text/JSON shape (the MCP tool always returns plain " +
				"text). Pass --watch to keep the table open and live-reload " +
				"local session state every --interval (default 2s); --watch " +
				"requires a TTY and refreshes local sessions only (remote rows " +
				"are fetched once at startup).",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: []command.Param{
			{Name: "closed", Type: command.Bool, Description: "Include closed sessions (tombstones and dangling symlinks)"},
			{Name: "watch", Short: 'w', Type: command.Bool, Description: "Continuously reload and re-render the session table (CLI only; requires a TTY)"},
			{Name: "interval", Type: command.String, Description: "Watch refresh interval as a Go duration, e.g. 5s (CLI only; default 2s)"},
		},
		Run: func(ctx context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
			var p struct {
				globalArgs
				Closed bool `json:"closed"`
			}
			_ = json.Unmarshal(args, &p)
			return runListResult(ctx, p.Closed, p.FormatOrDefault(), p.debugLogger(), remotesForCwd())
		},
		RunCLI: func(ctx context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				Closed   bool   `json:"closed"`
				Watch    bool   `json:"watch"`
				Interval string `json:"interval"`
			}
			_ = json.Unmarshal(args, &p)
			return runListCLI(ctx, p.globalArgs, p.Closed, p.Watch, p.Interval, remotesForCwd())
		},
	})

	app.AddCommand(&command.Command{
		Name:  "validate",
		Title: "Validate Sweatfile Hierarchy",
		Description: command.Description{
			Short: "Validate the sweatfile hierarchy",
			Long:  "Walk the sweatfile hierarchy from PWD and validate each file for structural and semantic correctness. Outputs TAP-14 with subtests.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Run: func(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
			cwd, err := os.Getwd()
			if err != nil {
				return command.TextErrorResult(err.Error()), nil
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return command.TextErrorResult(err.Error()), nil
			}
			out, exitCode := validate.RunString(home, cwd)
			if exitCode != 0 {
				return &command.Result{Text: out, IsErr: true}, nil
			}
			return command.TextResult(out), nil
		},
		RunCLI: func(_ context.Context, _ json.RawMessage) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			exitCode := validate.Run(os.Stdout, home, cwd)
			if exitCode != 0 {
				return fmt.Errorf("validation failed")
			}
			return nil
		},
	})

	app.AddCommand(&command.Command{
		Name: "clean",
		Description: command.Description{
			Short: "Remove merged worktrees",
			Long:  "Scan all worktrees, identify those whose branches are fully merged into the main branch, and remove them. Use -i to interactively handle dirty worktrees.",
		},
		Params: []command.Param{
			{
				Name:        "interactive",
				Short:       'i',
				Type:        command.Bool,
				Description: "Interactively discard changes in dirty merged worktrees",
			},
			{
				Name:        "dry-run",
				Short:       'n',
				Type:        command.Bool,
				Description: "Show what would be cleaned without removing anything",
			},
			{
				Name:        "yes",
				Short:       'y',
				Type:        command.Bool,
				Description: "Skip confirmation prompt",
			},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				Interactive bool `json:"interactive"`
				DryRun      bool `json:"dry-run"`
				Yes         bool `json:"yes"`
			}
			_ = json.Unmarshal(args, &p)

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return clean.Run(cwd, p.Interactive, p.DryRun, p.Yes, p.FormatOrDefault())
		},
	})

	app.AddCommand(&command.Command{
		Name: "pull",
		Description: command.Description{
			Short: "Pull repos and rebase worktrees",
			Long:  "Pull all clean repos, then rebase all clean worktrees onto their repo's default branch. Use -d to include dirty repos and worktrees.",
		},
		Params: []command.Param{
			{
				Name:        "dirty",
				Short:       'd',
				Type:        command.Bool,
				Description: "Include dirty repos and worktrees",
			},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				Dirty bool `json:"dirty"`
			}
			_ = json.Unmarshal(args, &p)

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return pull.Run(cwd, p.Dirty, p.FormatOrDefault())
		},
	})

	app.AddCommand(&command.Command{
		Name: "fork",
		Description: command.Description{
			Short: "Fork current worktree into a new branch",
			Long:  "Create a new worktree branched from the current worktree's HEAD. If new-branch is omitted, a name is auto-generated as <current-branch>-N. Resolves the source worktree from the current directory or --from flag. Does not attach to the new session. (The former --brief detached-worker mode was removed in spinclass#262 — use `sc spawn` (its repo arg is now optional) to launch a detached worker in THIS repo.)",
		},
		Params: []command.Param{
			{Name: "new-branch", Type: command.String, Description: "Name for the forked branch (auto-generated if omitted); must not contain '.' (reserved as the fleet room-JID component separator)"},
			{Name: "from", Type: command.String, Description: "Source worktree directory to fork from", Completer: completeWorktreeTargets},
			{Name: "brief", Type: command.String, Description: "REMOVED (spinclass#262): the detached-worker fork is gone. Use `sc spawn --brief \"...\"` (repo now optional) to launch a detached worker in this repo."},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				From      string `json:"from"`
				NewBranch string `json:"new-branch"`
				Brief     string `json:"brief"`
			}
			_ = json.Unmarshal(args, &p)

			if p.Brief != "" {
				return errors.New(
					"`sc fork --brief` (detached worker) was removed in spinclass#262; use `sc spawn --brief \"...\"` instead — its repo arg is now optional, so it launches a detached worker in THIS repo (a fresh worktree off the default branch)",
				)
			}

			sourceDir := p.From
			if sourceDir == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				sourceDir = cwd
			}

			source, err := resolveForkSource(sourceDir)
			if err != nil {
				return err
			}
			return shop.Fork(os.Stdout, source, p.NewBranch, p.FormatOrDefault())
		},
	})

	app.AddCommand(&command.Command{
		Name: "update-description",
		Description: command.Description{
			Short: "Update the description of a session",
			Long:  "Update the freeform description of an existing session. With --id, targets a specific worktree by directory name. Without --id, auto-detects from the current working directory. Multi-word descriptions must be quoted.",
		},
		Params: []command.Param{
			{Name: "description", Type: command.String, Description: "New description (quote multi-word strings)", Required: true},
			{Name: "id", Type: command.String, Description: "Session target to update (worktree directory name or <repo>/<branch> session key); auto-detects from cwd if omitted", Completer: completeWorktreeTargets},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				Description string `json:"description"`
				ID          string `json:"id"`
			}
			_ = json.Unmarshal(args, &p)
			return runUpdateDescription(p.Description, p.ID)
		},
	})
}

// runUpdateDescription is the update-description CLI body, extracted for
// testability (#139).
func runUpdateDescription(description, id string) error {
	var state *session.State
	var err error

	if id != "" {
		state, err = session.FindByTarget(id)
	} else {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return cwdErr
		}
		// Mirror the MCP handler's fallback (#137): a main checkout's live
		// implicit session is keyed by the randID in its state-<rand>.json
		// filename, which only FindImplicitAtCwd recovers.
		if !worktree.IsWorktree(cwd) {
			implicit, randID, ferr := session.FindImplicitAtCwd(cwd)
			if ferr == nil && implicit != nil {
				implicit.Description = description
				return session.WriteImplicit(*implicit, randID)
			}
		}
		state, err = session.FindByWorktreePath(cwd)
		// A worktree with no tracked state (harness run directly, never via
		// sc start/resume) auto-heals into a minimal session rather than
		// erroring — same path the MCP tool takes (#161); shared helper so
		// CLI and tool can't drift (#139).
		if err != nil && worktree.IsWorktree(cwd) {
			repoPath, rerr := git.CommonDir(cwd)
			branch, berr := git.BranchCurrent(cwd)
			if rerr == nil && berr == nil {
				state, err = resolveOrHealWorktreeState(repoPath, branch)
			}
		}
	}
	if err != nil {
		return err
	}

	// An implicit state resolved any other way (--id by session key, a
	// checkout subdirectory, a dead-PID survivor) cannot go through
	// session.Write: that keys <WorktreePath>/.spinclass/state.json and
	// would litter a stray file next to the real state-<rand>.json while
	// leaving the live session untouched (#139).
	if state.Kind == session.KindImplicit {
		return fmt.Errorf(
			"%s is an implicit (main-checkout) session; run `sc update-description` without --id from %s",
			state.Key(), state.WorktreePath,
		)
	}

	state.Description = description
	return session.Write(*state)
}

// gatherListData collects the raw inputs every `sc list` renderer shares:
// all local index states (unfiltered — the abandoned/closed filter is
// applied at render time so each renderer can honour its own `closed`
// flag) plus the parallel-queried remote rows and their per-host
// diagnostics. The MCP/text path (runListResult), the pretty CLI table,
// and the --watch model all source from here. An index read failure is
// the only hard error; remote failures degrade to diagnostics inside
// queryRemotes.
func gatherListData(ctx context.Context, dbg *slog.Logger, remotes []sweatfile.Remote) ([]session.State, []session.ListRow, []string, error) {
	states, err := session.ListAll(dbg)
	if err != nil {
		return nil, nil, nil, err
	}
	remoteRows, diags := queryRemotes(ctx, remotes, dbg)
	return states, remoteRows, diags, nil
}

// runListResult builds the `sc list` output. When closed is true,
// tombstones and dangling-symlink entries are included. Live entries
// always appear; abandoned/closed entries are filtered unless closed.
// format selects the rendering: "json" emits a session.ListRow array
// (the remote wire format); any other value (tap, table, default) emits
// the tab-separated text rows. dbg, when non-nil, is forwarded to
// session.ListAll so excluded index entries are logged at Debug level.
// remotes, when non-empty, are queried in parallel and their rows
// rendered after the local rows; an unreachable host yields one
// diagnostic line (text formats) or a Debug log (json must stay a
// clean array) — never a command failure.
// clownCountsByKey reads clown's presence index once and returns the live-clown
// count per spinclass session key (decoration). Shared by every `sc list` path
// so the presence read and its `now` are consistent within a single render.
func clownCountsByKey(now time.Time) map[string]int {
	out := map[string]int{}
	for key, ps := range clown.PresenceByDecoration(now) {
		out[key] = len(ps)
	}
	return out
}

func runListResult(ctx context.Context, closed bool, format string, dbg *slog.Logger, remotes []sweatfile.Remote) (*command.Result, error) {
	states, remoteRows, diags, err := gatherListData(ctx, dbg, remotes)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	counts := clownCountsByKey(time.Now())
	if format == "json" {
		if dbg != nil {
			for _, d := range diags {
				dbg.Debug("remote list diagnostic", "diag", d)
			}
		}
		data, err := json.Marshal(append(session.ListRowsWithClowns(states, closed, counts), remoteRows...))
		if err != nil {
			return command.TextErrorResult(err.Error()), nil
		}
		return command.TextResult(string(data) + "\n"), nil
	}
	var b strings.Builder
	for _, s := range states {
		isClosed := s.ResolveState() == session.StateAbandoned
		if isClosed && !closed {
			continue
		}
		resolved := s.ResolveDisplayState(counts[s.SessionKey])
		marker := ""
		if s.IsTombstone() {
			marker = "tombstone"
		} else if isClosed {
			marker = "dangling"
		} else if s.Kind == session.KindImplicit {
			marker = "main"
		}
		exited := ""
		if s.ExitedAt != nil {
			exited = s.ExitedAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s%s\n",
			s.SessionKey, resolved, marker, s.Branch, exited, s.WorktreePath, s.Description,
			spawnedBySuffix(s.SpawnedBy))
	}
	for _, r := range remoteRows {
		fmt.Fprintf(&b, "%s:%s\t%s\t\t%s\t\t\t%s%s\n",
			r.Remote, r.ID, r.State, r.Branch, r.Description, spawnedBySuffix(r.SpawnedBy))
	}
	for _, d := range diags {
		fmt.Fprintln(&b, d)
	}
	return command.TextResult(b.String()), nil
}

// spawnedBySuffix renders the spawn lineage hint appended to `sc list`
// text rows: a trailing `spawned-by:<driver-key>` column for sessions
// launched by `sc spawn` (FDR 0006), empty otherwise so
// non-spawned rows keep the legacy shape.
func spawnedBySuffix(key string) string {
	if key == "" {
		return ""
	}
	return "\tspawned-by:" + key
}

// remotesForCwd returns the [[remotes]] entries from the merged sweatfile
// hierarchy for the current working directory. Any load failure (including
// running where no hierarchy resolves) degrades to no remotes — `sc list`
// must keep working unchanged without remote config.
func remotesForCwd() []sweatfile.Remote {
	merged, ok := mergedSweatfileForCwd()
	if !ok {
		return nil
	}
	return merged.ActiveRemotes()
}

// queryRemotes fans out remote.QueryHost over the configured remotes in
// parallel (one goroutine per host, results into per-index slots) and
// flattens the outcomes in config order: healthy hosts contribute rows
// tagged with their remote name plus a completion-cache write; unreachable
// hosts contribute one single-line diagnostic each. Per-host isolation —
// neither a host error nor a cache-write failure is ever returned as an
// error; cache failures are logged to dbg (nil = silent) and dropped.
func queryRemotes(ctx context.Context, remotes []sweatfile.Remote, dbg *slog.Logger) ([]session.ListRow, []string) {
	if len(remotes) == 0 {
		return nil, nil
	}
	type outcome struct {
		rows []session.ListRow
		err  error
	}
	outcomes := make([]outcome, len(remotes))
	var wg sync.WaitGroup
	for i, r := range remotes {
		wg.Add(1)
		go func(i int, r sweatfile.Remote) {
			defer wg.Done()
			rows, err := remote.QueryHost(ctx, r)
			outcomes[i] = outcome{rows: rows, err: err}
		}(i, r)
	}
	wg.Wait()

	var rows []session.ListRow
	var diags []string
	for i, r := range remotes {
		if outcomes[i].err != nil {
			diags = append(diags, fmt.Sprintf("%s: unreachable (%s)", r.Name, shortErr(outcomes[i].err)))
			continue
		}
		// Cache the wire rows as served (before tagging) so the
		// completion cache stays in the host's own format.
		if err := remote.WriteCache(r.Name, outcomes[i].rows); err != nil && dbg != nil {
			dbg.Debug("remote cache write failed", "remote", r.Name, "error", err)
		}
		for j := range outcomes[i].rows {
			outcomes[i].rows[j].Remote = r.Name
		}
		rows = append(rows, outcomes[i].rows...)
	}
	return rows, diags
}

// shortErr collapses an error to its first trimmed line so a per-host
// diagnostic stays a single list row.
func shortErr(err error) string {
	msg := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	return msg
}
