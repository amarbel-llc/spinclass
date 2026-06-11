package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/protocol"
	"github.com/amarbel-llc/spinclass/internal/clean"
	"github.com/amarbel-llc/spinclass/internal/pull"
	"github.com/amarbel-llc/spinclass/internal/remote"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/shop"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/validate"
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
				"remote rows.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: []command.Param{
			{Name: "closed", Type: command.Bool, Description: "Include closed sessions (tombstones and dangling symlinks)"},
		},
		Run: func(ctx context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
			var p struct {
				globalArgs
				Closed bool `json:"closed"`
			}
			_ = json.Unmarshal(args, &p)
			return runListResult(ctx, p.Closed, p.FormatOrDefault(), p.debugLogger(), remotesForCwd())
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
			Long:  "Create a new worktree branched from the current worktree's HEAD. If new-branch is omitted, a name is auto-generated as <current-branch>-N. Resolves the source worktree from the current directory or --from flag. Does not attach to the new session. With --brief, the fork instead launches as a detached, harness-booted worker session (FDR 0006): the new worktree is created the same way, then booted via the [session-entry].spawn template and the command blocks for the worker's SessionStart chat hello.",
		},
		Params: []command.Param{
			{Name: "new-branch", Type: command.String, Description: "Name for the forked branch (auto-generated if omitted)"},
			{Name: "from", Type: command.String, Description: "Source worktree directory to fork from", Completer: completeWorktreeTargets},
			{Name: "brief", Type: command.String, Description: "Detached-fork brief: when set, the forked worktree is launched as a detached, harness-booted worker (FDR 0006) seeded with this brief, and the command blocks for the worker's chat hello. Omit for the classic create-only fork."},
			{Name: "description", Type: command.String, Description: "Session description for the detached worker (shows in `sc list`); only used with --brief"},
			{Name: "hello-timeout", Type: command.String, Description: "How long to wait for the worker's SessionStart hello, as a Go duration (e.g. \"90s\"). Default 60s. Only used with --brief."},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				From string `json:"from"`
				forkDetachedParams
			}
			_ = json.Unmarshal(args, &p)

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

			if p.Brief == "" {
				return shop.Fork(os.Stdout, source, p.NewBranch, p.FormatOrDefault())
			}

			res, driverKey, err := runForkDetached(source, p.forkDetachedParams)
			if err != nil {
				return err
			}
			fmt.Println(spawnResultText(driverKey, res))
			return nil
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

			var state *session.State
			var err error

			if p.ID != "" {
				state, err = session.FindByTarget(p.ID)
			} else {
				cwd, cwdErr := os.Getwd()
				if cwdErr != nil {
					return cwdErr
				}
				state, err = session.FindByWorktreePath(cwd)
			}
			if err != nil {
				return err
			}

			state.Description = p.Description
			return session.Write(*state)
		},
	})
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
func runListResult(ctx context.Context, closed bool, format string, dbg *slog.Logger, remotes []sweatfile.Remote) (*command.Result, error) {
	states, err := session.ListAll(dbg)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	remoteRows, diags := queryRemotes(ctx, remotes, dbg)
	if format == "json" {
		if dbg != nil {
			for _, d := range diags {
				dbg.Debug("remote list diagnostic", "diag", d)
			}
		}
		data, err := json.Marshal(append(session.ListRows(states, closed), remoteRows...))
		if err != nil {
			return command.TextErrorResult(err.Error()), nil
		}
		return command.TextResult(string(data) + "\n"), nil
	}
	var b strings.Builder
	for _, s := range states {
		resolved := s.ResolveState()
		isClosed := resolved == session.StateAbandoned
		if isClosed && !closed {
			continue
		}
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
// launched by `sc spawn` / detached fork (FDR 0006), empty otherwise so
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
