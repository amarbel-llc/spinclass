package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/amarbel-llc/crap/go-crap/v2/crap"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/spinclass/internal/check"
	spinclose "github.com/amarbel-llc/spinclass/internal/close"
	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/merge"
	"github.com/amarbel-llc/spinclass/internal/present"
	"github.com/amarbel-llc/spinclass/internal/remote"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sessionpick"
	"github.com/amarbel-llc/spinclass/internal/shop"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/sweatfileio"
	"github.com/amarbel-llc/spinclass/internal/worktree"
	"github.com/mattn/go-isatty"
)

func registerSessionCommands(app *command.App) {
	app.AddCommand(&command.Command{
		Name: "start",
		Description: command.Description{
			Short: "Create and start a new worktree session",
			Long:  "Create a new worktree with a random branch name and start a session. The optional description is a freeform string used to label the session; quote multi-word descriptions.",
		},
		Params: []command.Param{
			{Name: "description", Type: command.String, Description: "Freeform session description (quote multi-word strings)"},
			{Name: "merge-on-close", Type: command.Bool, Description: "Auto-merge worktree into default branch on session close"},
			{Name: "no-attach", Type: command.Bool, Description: "Create worktree but skip attaching"},
		},
		RunCLI: runStart,
	})

	// `start-gh_pr` and `start-gh_issue` are registered dynamically from
	// sweatfile.GetDefault()'s baked-in [[start-commands]] entries via
	// registerPluginStartCommands(). See internal/sweatfile/sweatfile.go
	// defaultStartCommands().

	app.AddCommand(&command.Command{
		Name: "spawn",
		Description: command.Description{
			Short: "Spawn a detached worker session in a sibling repo",
			Long: "Launch a detached, harness-booted worker session in a DIFFERENT repo (FDR 0006). " +
				"The target is a repo dirname leaf-searched under $HOME/*/repos/<name> (or an explicit path); " +
				"the worker repo's sweatfile [session-entry].spawn / spawn-entry templates decide the multiplexer and harness. " +
				"Spawn creates the worker worktree and session state (with spawned_by lineage), execs the spawn template detached, " +
				"then blocks up to the hello deadline (60s default; --hello-timeout tunes it) for the worker's SessionStart chat hello. " +
				"The brief is the worker's ONLY context — include everything it needs plus an explicit instruction to message you back " +
				"via chat when done (the printed session_key is the worker's chat address; yours is its reply target). " +
				"--issue prepends a GitHub issue's title and body (fetched in the target repo via `gh issue view`) to the brief. " +
				"On a hello timeout the worker worktree and state are intentionally left behind for inspection; clean up with `sc close`.",
		},
		Params: spawnParamList(),
		RunCLI: runSpawnCLI,
	})

	app.AddCommand(&command.Command{
		Name: "resume",
		Description: command.Description{
			Short: "Resume an existing worktree session",
			Long:  "Resume an existing worktree session. With no argument, auto-detects from the current working directory; if cwd isn't inside a tracked session, prompts interactively when stdin is a TTY or errors with the list of available session IDs otherwise. Auto-detected and single-candidate sessions show a confirmation dialog before attaching; -y/--yes skips it, except when the session is active (live PID, probably attached elsewhere), which always warns with default Cancel. With one argument, resumes the session matching the target — a worktree directory name or a <repo>/<branch> session key as printed by `sc list` — without any dialog; naming the target is the confirmation. A bare name matching sessions in several repos errors with the colliding session keys. Tab completion offers the current repo's sessions by bare name plus any session whose repo sits beneath the cwd by session key (a cwd above nested repos sees them all); outside any repo it offers all non-abandoned sessions (labels include the repo basename to disambiguate). A host:-prefixed target naming a configured [[remotes]] entry routes over that remote's attach template instead of resolving locally, and completion and the interactive picker additionally offer cached remote sessions under the host: prefix (selecting a remote row routes the same way, no dialog); see spinclass-sweatfile(5) [[remotes]].",
		},
		Params: []command.Param{
			{Name: "id", Type: command.String, Description: "Session target (worktree directory name or <repo>/<branch> session key from `sc list`); auto-detects from cwd if omitted", Completer: completeWorktreeTargets},
			{Name: "no-attach", Type: command.Bool, Description: "Find session but skip attaching"},
			{Name: "yes", Short: 'y', Type: command.Bool, Description: "Skip the resume confirmation dialog"},
		},
		RunCLI: runResume,
	})

	app.AddCommand(&command.Command{
		Name: "merge",
		Description: command.Description{
			Short: "Merge a worktree into main",
			Long:  "Merge a worktree branch into the main repo with --ff-only and remove the worktree. When run from inside a worktree, merges that worktree. When run from the main repo, specify a target or choose interactively. Output formats: auto (default; live viewport on a TTY, ndjson-crap records when piped), viewport, plain (verdict lines), or ndjson. TAP is retired for merge/check.",
		},
		Params: []command.Param{
			{Name: "target", Type: command.String, Description: "Target worktree to merge: a worktree directory name or <repo>/<branch> session key from `sc list` (interactive selection if omitted)", Completer: completeWorktreeTargets},
			{Name: "local-only", Type: command.Bool, Description: "Merge into the LOCAL default branch only — skip the pull-before and push-after. Default is to pull+push so the merge reaches origin (#126)."},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				Target    string `json:"target"`
				LocalOnly bool   `json:"local-only"`
			}
			_ = json.Unmarshal(args, &p)

			if err := rejectRemoteTarget(p.Target, remotesForTarget(p.Target)); err != nil {
				return err
			}

			// merge.Run resolves the format itself: pass the RAW --format
			// value ("" means auto — viewport on a TTY, ndjson when piped).
			// git_sync now defaults ON (push by default, #126); --local-only
			// is the explicit opt-out.
			return merge.Run(executor.ShellExecutor{}, p.Format, p.Target, !p.LocalOnly)
		},
	})

	app.AddCommand(&command.Command{
		Name: "check",
		Description: command.Description{
			Short: "Run the [hooks].pre-merge command without merging",
			Long:  "Runs the configured [hooks].pre-merge command (the agent-CI hook) in the current worktree. Reports ok / not ok and exits non-zero on failure. Available regardless of [hooks].disable-merge. Output formats: auto (default; live viewport on a TTY, ndjson-crap records when piped), viewport, plain (verdict lines), or ndjson. TAP is retired for merge/check.",
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
			}
			_ = json.Unmarshal(args, &p)

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			resolved, rerr := present.ResolveFormat(p.Format, isatty.IsTerminal(os.Stdout.Fd()))
			if rerr != nil {
				return rerr
			}
			return present.WithReporter(resolved, "check", os.Stdout, os.Stderr, func(rep *crap.Reporter) error {
				_, err := check.Run(rep, cwd)
				return err
			})
		},
	})

	app.AddCommand(&command.Command{
		Name: "close",
		Description: command.Description{
			Short: "Close a session without merging",
			Long:  "Remove a worktree and its branch without merging into main. With no argument, closes the current worktree if cwd is inside one, otherwise prompts interactively when stdin is a TTY (or errors with the list of session IDs). With one argument, closes the named session; orphaned git worktrees without a spinclass state file are rejected with a hint to run `git worktree remove`. Prompts for confirmation if the branch has unintegrated commits or uncommitted changes; use --force to skip.",
		},
		Params: []command.Param{
			{Name: "target", Type: command.String, Description: "Target session (worktree directory name or <repo>/<branch> session key from `sc list`); auto-detects from cwd if omitted", Completer: completeWorktreeTargets},
			{Name: "force", Short: 'f', Type: command.Bool, Description: "Skip confirmation for unpushed branches"},
			{Name: "nix-gc", Type: command.String, Description: "Override [hooks].disable-nix-gc for this invocation: 'true' forces worktree-scoped Nix gc, 'false' skips it"},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				Target string `json:"target"`
				Force  bool   `json:"force"`
				NixGC  string `json:"nix-gc"`
			}
			_ = json.Unmarshal(args, &p)

			nixGCOverride, err := parseNixGCFlag(p.NixGC)
			if err != nil {
				return err
			}

			if err := rejectRemoteTarget(p.Target, remotesForTarget(p.Target)); err != nil {
				return err
			}

			return spinclose.Run(os.Stdout, p.Target, p.Force, nixGCOverride, p.FormatOrDefault(), p.debugLogger())
		},
	})
}

// parseNixGCFlag turns the raw --nix-gc=<value> argument into a *bool
// override consumed by close.Run. Empty string means "defer to sweatfile"
// (returns nil); "true"/"false" return pointers to the parsed boolean. Any
// other value is rejected with a user-facing error so typos surface
// immediately instead of silently falling through to default behavior.
func parseNixGCFlag(raw string) (*bool, error) {
	switch raw {
	case "":
		return nil, nil
	case "true":
		v := true
		return &v, nil
	case "false":
		v := false
		return &v, nil
	default:
		return nil, fmt.Errorf("--nix-gc must be 'true' or 'false', got %q", raw)
	}
}

// completeWorktreeTargets returns session targets keyed to descriptive
// labels for tab completion. Inside a repo the scope is that repo's
// sessions plus any session whose repo sits beneath the cwd
// (session.ListForScope — a cwd above nested repos, e.g. ~/eng over
// ~/eng/repos/*, sees the nested repos' sessions too); outside any
// repo it includes every non-abandoned session. Containing-repo
// sessions are keyed by bare worktree dirname; sessions from other
// repos by their `<repo>/<branch>` session key — the same strings
// `sc list` prints, which FindByTarget accepts. Labels are the picker
// rows' Detail strings (sessionpick.ItemForState), so completion and
// the interactive picker read identically. Output is sorted via
// session.SortStates so the active session shows up first.
func completeWorktreeTargets() map[string]string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}

	var sessions []session.State
	repoPath, repoErr := worktree.DetectRepo(cwd)
	if repoErr == nil {
		sessions, _ = session.ListForScope(repoPath, cwd, nil)
	} else {
		all, err := session.ListAll(nil)
		if err != nil {
			return nil
		}
		for _, s := range all {
			if s.ResolveState() != session.StateAbandoned {
				sessions = append(sessions, s)
			}
		}
	}
	session.SortStates(sessions)

	now := time.Now()
	result := make(map[string]string, len(sessions))
	for _, s := range sessions {
		key := filepath.Base(s.WorktreePath)
		if repoErr == nil && s.RepoPath != repoPath {
			// Reached via cwd-prefix matching: bare dirnames are only
			// unambiguous within one repo, so offer the session key.
			key = s.Key()
		}
		result[key] = sessionpick.ItemForState(s, now).Detail
	}
	for key, label := range completeRemoteTargets(remotesForCwd()) {
		result[key] = label
	}
	return result
}

// completeRemoteTargets returns `<remote>:<id>`-keyed completion entries
// built from the per-remote cache files ONLY — no ssh, no network (the
// cache is refreshed by each `sc list`; see
// docs/plans/2026-06-06-remote-sessions-design.md). Labels are the
// picker rows' Detail strings (sessionpick.ItemForRemoteRow), so
// completion and the interactive picker read identically.
func completeRemoteTargets(remotes []sweatfile.Remote) map[string]string {
	if len(remotes) == 0 {
		return nil
	}
	result := make(map[string]string)
	for name, rows := range remote.ReadAllCaches(remotes) {
		for _, r := range rows {
			it := sessionpick.ItemForRemoteRow(name, r)
			result[it.Target] = it.Detail
		}
	}
	return result
}

// remoteResumeArgv is the resume routing seam: when target parses as
// host:id AND the host names a configured remote, it returns the attach
// argv ({ssh}/{id} substituted) and true. Everything else — plain local
// targets, prefixes matching no configured remote — returns false so the
// caller falls through to local resolution unchanged (a bare name
// containing ':' that isn't a configured remote behaves exactly as today).
func remoteResumeArgv(target string, remotes []sweatfile.Remote) ([]string, bool) {
	r, id, ok := matchRemoteTarget(target, remotes)
	if !ok {
		return nil, false
	}
	return remote.AttachArgv(r, id), true
}

// rejectRemoteTarget guards verbs that don't support remote targets in v1
// (close, merge): a host:-prefixed target naming a configured remote is
// rejected explicitly instead of mis-resolving locally. Unconfigured
// prefixes and plain targets return nil — behavior unchanged.
func rejectRemoteTarget(target string, remotes []sweatfile.Remote) error {
	if _, _, ok := matchRemoteTarget(target, remotes); ok {
		return errors.New("remote targets support resume only (v1)")
	}
	return nil
}

// remotesForTarget gates the sweatfile-hierarchy load behind the target
// grammar: plain local targets (the overwhelmingly common case) parse false
// via the pure-regex ParseTarget and skip the config walk entirely; only
// host:-shaped targets pay for remotesForCwd().
func remotesForTarget(target string) []sweatfile.Remote {
	if _, _, ok := remote.ParseTarget(target); !ok {
		return nil
	}
	return remotesForCwd()
}

// remoteAttachPlan decides what resume does with a possibly-remote target:
// handled=false means local resolution proceeds as today; handled=true with
// an error rejects an unsupported combination (--no-attach has no remote
// meaning — there is nothing local to skip attaching to); handled=true with
// argv means exec the remote attach.
func remoteAttachPlan(target string, noAttach bool, remotes []sweatfile.Remote) (argv []string, handled bool, err error) {
	argv, ok := remoteResumeArgv(target, remotes)
	if !ok {
		return nil, false, nil
	}
	if noAttach {
		return nil, true, errors.New("--no-attach is not supported for remote targets")
	}
	return argv, true, nil
}

// matchRemoteTarget parses target as host:id and resolves the host against
// the configured remotes by name. ok is false when the target doesn't parse
// or no remote matches.
func matchRemoteTarget(target string, remotes []sweatfile.Remote) (sweatfile.Remote, string, bool) {
	host, id, ok := remote.ParseTarget(target)
	if !ok {
		return sweatfile.Remote{}, "", false
	}
	for _, r := range remotes {
		if r.Name == host {
			return r, id, true
		}
	}
	return sweatfile.Remote{}, "", false
}

// runRemoteAttach execs the remote attach argv as the session process,
// using the same mechanism as the local executors (exec.Command with full
// stdio/TTY passthrough; see executor.ShellExecutor) rather than inventing
// a new one. The remote spinclass owns sweatfile/entrypoint semantics from
// there; attach failures pass through the exec'd command's error. Thin
// untested glue, mirroring attachSession's exec path.
func runRemoteAttach(argv []string) error {
	cmd := osexec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

type startArgs struct {
	globalArgs
	Description  string `json:"description"`
	MergeOnClose bool   `json:"merge-on-close"`
	NoAttach     bool   `json:"no-attach"`
}

func attachSession(resolvedPath worktree.ResolvedPath, args startArgs) error {
	repoPath := resolvedPath.RepoPath

	hierarchy, err := sweatfileio.LoadWorktreeHierarchy(os.Getenv("HOME"), repoPath, resolvedPath.AbsPath)
	if err != nil {
		hierarchy, err = sweatfileio.LoadHierarchy(os.Getenv("HOME"), repoPath)
		if err != nil {
			return err
		}
	}

	merged := hierarchy.Merged
	exec := executor.SessionExecutor{
		Entrypoint:  merged.SessionStart(),
		Description: resolvedPath.Description,
		Env:         merged.SessionEnv(),
	}

	return shop.Attach(
		os.Stdout,
		exec,
		resolvedPath,
		merged,
		args.FormatOrDefault(),
		args.MergeOnClose,
		args.NoAttach,
		args.Verbose,
	)
}

func runStart(_ context.Context, args json.RawMessage) error {
	var p startArgs
	_ = json.Unmarshal(args, &p)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	repoPath, err := worktree.DetectRepo(cwd)
	if err != nil {
		return err
	}

	var descArgs []string
	if p.Description != "" {
		descArgs = []string{p.Description}
	}

	resolvedPath, err := worktree.ResolvePath(repoPath, descArgs)
	if err != nil {
		return err
	}

	return attachSession(resolvedPath, p)
}

func runResume(_ context.Context, args json.RawMessage) error {
	var p struct {
		globalArgs
		ID       string `json:"id"`
		NoAttach bool   `json:"no-attach"`
		Yes      bool   `json:"yes"`
	}
	_ = json.Unmarshal(args, &p)

	// host:-prefixed targets naming a configured remote route over the
	// remote's attach template; everything else resolves locally as today.
	if argv, handled, err := remoteAttachPlan(p.ID, p.NoAttach, remotesForTarget(p.ID)); handled {
		if err != nil {
			return err
		}
		return runRemoteAttach(argv)
	}

	var state *session.State
	var err error

	// targetAffirmed: the user already affirmed the target by naming an
	// explicit id or by picking it from the multi-match picker — those
	// paths skip the confirm dialog (resumeConfirmPlan's explicitTarget).
	// Auto-detect from cwd and the picker's single-match short-circuit
	// leave it false and confirm before attaching.
	targetAffirmed := true

	if p.ID != "" {
		state, err = session.FindByTarget(p.ID)
	} else {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return cwdErr
		}
		state, err = session.FindByWorktreePath(cwd)
		if err == nil {
			targetAffirmed = false
		} else {
			repoPath, repoErr := worktree.DetectRepo(cwd)
			if repoErr != nil {
				return err
			}
			// Cached remote sessions (FDR 0011) appear in the picker after
			// the local rows; they never count toward the single-match
			// shortcut, so a remote row is only reachable by explicit
			// selection.
			remotes := remotesForCwd()
			remoteRows := sessionpick.ItemsForRemoteRows(remote.ReadAllCaches(remotes))
			var picked *sessionpick.Item
			var autoPicked bool
			picked, autoPicked, err = sessionpick.ChooseAutoSingle(repoPath, "resume", p.debugLogger(), remoteRows)
			if err != nil {
				return err
			}
			if picked == nil {
				// Picker dismissed (q/esc/ctrl+c): clean exit, clown-style.
				return nil
			}
			if picked.State == nil {
				// Remote row picked: selection is the confirmation — route
				// over the remote's attach template, no dialog.
				argv, handled, attachErr := remoteAttachPlan(picked.Target, p.NoAttach, remotes)
				if attachErr != nil {
					return attachErr
				}
				if !handled {
					return fmt.Errorf("remote target %q matched no configured remote", picked.Target)
				}
				return runRemoteAttach(argv)
			}
			state = picked.State
			targetAffirmed = !autoPicked
		}
	}
	if err != nil {
		return err
	}

	resolvedState := state.ResolveState()
	kind, err := resumeConfirmPlan(resolvedState, targetAffirmed, p.Yes, isInteractiveTerminal())
	if err != nil {
		return err
	}
	if kind != resumeConfirmNone {
		ok, err := confirmResume(state, resolvedState, kind)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	hierarchy, err := sweatfileio.LoadWorktreeHierarchy(os.Getenv("HOME"), state.RepoPath, state.WorktreePath)
	if err != nil {
		hierarchy, err = sweatfileio.LoadHierarchy(os.Getenv("HOME"), state.RepoPath)
		if err != nil {
			return err
		}
	}

	merged := hierarchy.Merged
	entrypoint := merged.SessionStart()
	if resume := merged.SessionResume(); resume != nil {
		entrypoint = resume
	}

	rp := worktree.ResolvedPath{
		AbsPath:     state.WorktreePath,
		RepoPath:    state.RepoPath,
		SessionKey:  state.SessionKey,
		Branch:      state.Branch,
		Description: state.Description,
	}

	exec := executor.SessionExecutor{
		Entrypoint:  entrypoint,
		Description: state.Description,
		Env:         merged.SessionEnv(),
	}

	// One-shot terminal title before the exec chain (#154); TTY-gated so
	// piped output stays clean.
	if !p.NoAttach && isatty.IsTerminal(os.Stdout.Fd()) {
		emitResumeTitle(os.Stdout, merged, state.SessionKey)
	}

	return shop.Attach(
		os.Stdout,
		exec,
		rp,
		merged,
		p.FormatOrDefault(),
		false,
		p.NoAttach,
		p.Verbose,
	)
}

// emitResumeTitle writes the OSC 2 terminal title for the session being
// resumed (#154). Spawned (FDR 0006) sessions' ptys have no title-writing
// shell, so without this the attaching terminal keeps its stale outer
// title; one shot suffices (an interactive shell inside an ordinary
// session overwrites it at its next prompt anyway).
func emitResumeTitle(w io.Writer, merged sweatfile.Sweatfile, sessionKey string) {
	tpl := merged.SessionResumeTitle()
	if tpl == "" {
		return
	}
	fmt.Fprintf(w, "\033]2;%s\007", strings.ReplaceAll(tpl, "{id}", sessionKey))
}
