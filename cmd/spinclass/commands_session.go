package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"os/signal"
	"path/filepath"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/spinclass/internal/chat"
	"github.com/amarbel-llc/spinclass/internal/check"
	spinclose "github.com/amarbel-llc/spinclass/internal/close"
	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/merge"
	"github.com/amarbel-llc/spinclass/internal/remote"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sessionpick"
	"github.com/amarbel-llc/spinclass/internal/shop"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/worktree"
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
		Name: "resume",
		Description: command.Description{
			Short: "Resume an existing worktree session",
			Long:  "Resume an existing worktree session. With no argument, auto-detects from the current working directory; if cwd isn't inside a tracked session, prompts interactively when stdin is a TTY or errors with the list of available session IDs otherwise. Auto-detected and single-candidate sessions show a confirmation dialog before attaching; -y/--yes skips it, except when the session is active (live PID, probably attached elsewhere), which always warns with default Cancel. With one argument, resumes the session whose worktree directory name matches without any dialog — naming the target is the confirmation. Tab completion offers session IDs scoped to the current repo when run inside one, or all non-abandoned sessions otherwise (labels include the repo basename to disambiguate). A host:-prefixed target naming a configured [[remotes]] entry routes over that remote's attach template instead of resolving locally, and completion additionally offers cached remote sessions under the host: prefix; see spinclass-sweatfile(5) [[remotes]].",
		},
		Params: []command.Param{
			{Name: "id", Type: command.String, Description: "Session ID (worktree directory name); auto-detects from cwd if omitted", Completer: completeWorktreeTargets},
			{Name: "no-attach", Type: command.Bool, Description: "Find session but skip attaching"},
			{Name: "yes", Short: 'y', Type: command.Bool, Description: "Skip the resume confirmation dialog"},
		},
		RunCLI: runResume,
	})

	app.AddCommand(&command.Command{
		Name: "merge",
		Description: command.Description{
			Short: "Merge a worktree into main",
			Long:  "Merge a worktree branch into the main repo with --ff-only and remove the worktree. When run from inside a worktree, merges that worktree. When run from the main repo, specify a target or choose interactively.",
		},
		Params: []command.Param{
			{Name: "target", Type: command.String, Description: "Target worktree to merge (interactive selection if omitted)", Completer: completeWorktreeTargets},
			{Name: "git-sync", Type: command.Bool, Description: "Pull and push after merge"},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
				Target  string `json:"target"`
				GitSync bool   `json:"git-sync"`
			}
			_ = json.Unmarshal(args, &p)

			if err := rejectRemoteTarget(p.Target, remotesForTarget(p.Target)); err != nil {
				return err
			}

			return merge.Run(executor.ShellExecutor{}, p.FormatOrDefault(), p.Target, p.GitSync, p.Verbose)
		},
	})

	app.AddCommand(&command.Command{
		Name: "check",
		Description: command.Description{
			Short: "Run the [hooks].pre-merge command without merging",
			Long:  "Runs the configured [hooks].pre-merge command (the agent-CI hook) in the current worktree. Reports ok / not ok and exits non-zero on failure. Available regardless of [hooks].disable-merge.",
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
			_, err = check.Run(os.Stdout, p.FormatOrDefault(), cwd, p.Verbose)
			return err
		},
	})

	app.AddCommand(&command.Command{
		Name: "chat-watch",
		Description: command.Description{
			Short: "Stream cross-session chat messages addressed to this session",
			Long:  "Watches the global chatroom and prints one line per new message addressed to this session (broadcasts plus direct messages to this session's key). Intended to be run as a Claude Code plugin monitor, which delivers each stdout line into the session as a notification. Resolves this session's key from $SPINCLASS_SESSION_ID, falling back to the current worktree. Runs until interrupted. When SPINCLASS_CHAT_WAKE=clown it exits immediately — clown's job-watch monitor owns the push path in that mode.",
		},
		RunCLI: func(ctx context.Context, _ json.RawMessage) error {
			return runChatWatch(ctx)
		},
	})

	app.AddCommand(&command.Command{
		Name: "close",
		Description: command.Description{
			Short: "Close a session without merging",
			Long:  "Remove a worktree and its branch without merging into main. With no argument, closes the current worktree if cwd is inside one, otherwise prompts interactively when stdin is a TTY (or errors with the list of session IDs). With one argument, closes the named session; orphaned git worktrees without a spinclass state file are rejected with a hint to run `git worktree remove`. Prompts for confirmation if the branch has unintegrated commits or uncommitted changes; use --force to skip.",
		},
		Params: []command.Param{
			{Name: "target", Type: command.String, Description: "Target session ID (worktree directory name); auto-detects from cwd if omitted", Completer: completeWorktreeTargets},
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

// runChatWatch is the chat-watch monitor body: it streams chatroom messages
// addressed to this session to stdout, one line each, until ctx is cancelled.
func runChatWatch(ctx context.Context) error {
	// In clown wake mode the push path is clown's job-watch monitor reading
	// the job-wakeup channel; this monitor stands down so each message
	// yields exactly one notification. Exit silently: a stdout line here
	// would itself become a notification.
	if chat.ResolveWakeMode() == chat.WakeModeClown {
		return nil
	}

	sessionKey, err := currentSessionKey()
	if err != nil {
		return err
	}

	sigCtx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	err = chat.Watch(sigCtx, sessionKey, func(m chat.Message) error {
		_, werr := fmt.Fprintln(os.Stdout, formatChatNotification(m))
		return werr
	})
	// A clean interrupt (ctx cancelled) is a normal monitor shutdown,
	// not an error.
	if err != nil && ctx.Err() != nil {
		return nil
	}
	return err
}

// formatChatNotification renders one chat message as a single notification
// line. The leading marker gives the agent a recognizable, greppable prefix;
// the from-key tells it who to reply to via chat-send. Only the subject is
// carried — notification events are truncated by the harness past a few
// hundred characters (#103) — with a chat-read hint appended when the
// message has a body beyond it.
func formatChatNotification(m chat.Message) string {
	line := fmt.Sprintf("[spinclass-chat] from %s: %s", m.From, m.DisplaySubject())
	if m.HasMoreThanSubject() {
		line += " · full body: " + m.RecoveryHint()
	}
	return line
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

// completeWorktreeTargets returns session IDs (worktree directory
// names) keyed to descriptive labels for tab completion. Inside a repo
// the list is scoped to that repo; outside any repo it includes every
// non-abandoned session and tags each label with its repo basename so
// duplicates across repos disambiguate. Output is sorted via
// session.SortStates so the active session shows up first.
func completeWorktreeTargets() map[string]string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}

	var sessions []session.State
	repoPath, err := worktree.DetectRepo(cwd)
	if err == nil {
		sessions, _ = session.ListForRepo(repoPath, nil)
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

	result := make(map[string]string, len(sessions))
	for _, s := range sessions {
		id := filepath.Base(s.WorktreePath)
		label := s.Branch
		if s.Description != "" {
			label = fmt.Sprintf("%s — %s", s.Branch, s.Description)
		}
		if repoPath == "" {
			label = fmt.Sprintf("%s (%s)", label, filepath.Base(s.RepoPath))
		}
		result[id] = label
	}
	for key, label := range completeRemoteTargets(remotesForCwd()) {
		result[key] = label
	}
	return result
}

// completeRemoteTargets returns `<remote>:<id>`-keyed completion entries
// built from the per-remote cache files ONLY — no ssh, no network (the
// cache is refreshed by each `sc list`; see
// docs/plans/2026-06-06-remote-sessions-design.md). Labels mirror the
// local `branch — description` style with the remote name appended as
// the disambiguating suffix; the state is prefixed because remote rows
// can't rely on local active-first sorting to convey it.
func completeRemoteTargets(remotes []sweatfile.Remote) map[string]string {
	if len(remotes) == 0 {
		return nil
	}
	result := make(map[string]string)
	for name, rows := range remote.ReadAllCaches(remotes) {
		for _, r := range rows {
			label := r.ID
			if r.Description != "" {
				label = fmt.Sprintf("%s — %s", r.ID, r.Description)
			}
			result[name+":"+r.ID] = fmt.Sprintf("[%s] %s (%s)", r.State, label, name)
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

	hierarchy, err := sweatfile.LoadWorktreeHierarchy(
		os.Getenv("HOME"), repoPath, resolvedPath.AbsPath,
	)
	if err != nil {
		hierarchy, err = sweatfile.LoadHierarchy(os.Getenv("HOME"), repoPath)
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
		state, err = session.FindByID(p.ID)
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
			var autoPicked bool
			state, autoPicked, err = sessionpick.ChooseAutoSingle(repoPath, "resume", p.debugLogger())
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

	hierarchy, err := sweatfile.LoadWorktreeHierarchy(
		os.Getenv("HOME"), state.RepoPath, state.WorktreePath,
	)
	if err != nil {
		hierarchy, err = sweatfile.LoadHierarchy(os.Getenv("HOME"), state.RepoPath)
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
