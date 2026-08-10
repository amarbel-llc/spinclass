package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/spinclass/internal/attestation"
	"code.linenisgreat.com/spinclass/internal/check"
	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/executor"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/hooks"
	"code.linenisgreat.com/spinclass/internal/job"
	"code.linenisgreat.com/spinclass/internal/merge"
	"code.linenisgreat.com/spinclass/internal/present"
	"code.linenisgreat.com/spinclass/internal/servelog"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// wrapMCPHandler adds entry/exit logging and panic recovery around an MCP
// tool handler. A panic becomes a TextErrorResult instead of a dead server
// (which in stdio mode means the MCP client sees the connection close and
// every subsequent tool call fails with `No such tool available: …`).
func wrapMCPHandler(
	name string,
	fn func(ctx context.Context, args json.RawMessage, p command.Prompter) (*command.Result, error),
) func(ctx context.Context, args json.RawMessage, p command.Prompter) (*command.Result, error) {
	return func(ctx context.Context, args json.RawMessage, p command.Prompter) (res *command.Result, err error) {
		start := time.Now()
		servelog.Infof("mcp.handler.enter name=%s args_size=%d", name, len(args))

		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				servelog.Errorf("mcp.handler.panic name=%s recovered=%v\n%s", name, r, stack)
				logPath := servelog.Path()
				msg := fmt.Sprintf("spinclass handler %q panicked: %v", name, r)
				if logPath != "" {
					msg += fmt.Sprintf(" (see %s)", logPath)
				}
				res = command.TextErrorResult(msg)
				err = nil
			}
			servelog.Infof("mcp.handler.exit name=%s dur=%s", name, time.Since(start))
		}()

		return fn(ctx, args, p)
	}
}

// registerMCPOnlyCommands registers commands that only make sense as MCP
// tools (not user-facing CLI commands). They are Hidden so they don't show
// up in help, but they're still exposed via RegisterMCPToolsV1 because the
// hidden filter only excludes them from CLI/help output, not MCP.
//
// Wait — actually VisibleCommands excludes Hidden from MCP too. So these
// commands are NOT marked Hidden; instead they have a recognizable
// "session-tool" prefix so users who run `sc help` understand they're
// agent-facing helpers.
func registerMCPOnlyCommands(app *command.App) {
	hookPreview := preMergeHookForCwd()
	preMergeSkills := preMergeSkillsForCwd()
	if mergeDisabledForCwd() {
		app.AddCommand(&command.Command{
			Name:  "check-this-session",
			Title: "Check This Session",
			Description: command.Description{
				Short: buildCheckThisSessionDescription(hookPreview),
			},
			Annotations: &protocol.ToolAnnotations{
				ReadOnlyHint:    protocol.BoolPtr(false),
				DestructiveHint: protocol.BoolPtr(false),
				IdempotentHint:  protocol.BoolPtr(false),
				OpenWorldHint:   protocol.BoolPtr(false),
			},
			Params: []command.Param{},
			Run:    wrapMCPHandler("check-this-session", handleCheckThisSession),
		})
		if clown.Enabled() {
			app.AddCommand(&command.Command{
				Name:  "check-this-session-async",
				Title: "Check This Session (async)",
				Description: command.Description{
					Short: buildCheckAsyncDescription(hookPreview),
				},
				Annotations: &protocol.ToolAnnotations{
					ReadOnlyHint:    protocol.BoolPtr(false),
					DestructiveHint: protocol.BoolPtr(false),
					IdempotentHint:  protocol.BoolPtr(false),
					OpenWorldHint:   protocol.BoolPtr(false),
				},
				Params: []command.Param{},
				Run:    wrapMCPHandler("check-this-session-async", handleCheckThisSessionAsync),
			})
		}
	} else {
		app.AddCommand(&command.Command{
			Name:  "merge-this-session",
			Title: "Merge This Session",
			Description: command.Description{
				Short: buildMergeThisSessionDescription(hookPreview),
			},
			Annotations: &protocol.ToolAnnotations{
				ReadOnlyHint:    protocol.BoolPtr(false),
				DestructiveHint: protocol.BoolPtr(true),
				IdempotentHint:  protocol.BoolPtr(false),
				OpenWorldHint:   protocol.BoolPtr(false),
			},
			Params: []command.Param{
				{Name: "local_only", Type: command.Bool, Description: localOnlyParamDesc},
				{Name: "default_branch", Type: command.String, Description: defaultBranchParamDesc},
			},
			Run: wrapMCPHandler("merge-this-session", handleMergeThisSession),
		})
		if clown.Enabled() {
			app.AddCommand(&command.Command{
				Name:  "merge-this-session-async",
				Title: "Merge This Session (async)",
				Description: command.Description{
					Short: buildMergeAsyncDescription(hookPreview),
				},
				Annotations: &protocol.ToolAnnotations{
					ReadOnlyHint:    protocol.BoolPtr(false),
					DestructiveHint: protocol.BoolPtr(true),
					IdempotentHint:  protocol.BoolPtr(false),
					OpenWorldHint:   protocol.BoolPtr(false),
				},
				Params: []command.Param{
					{Name: "local_only", Type: command.Bool, Description: localOnlyParamDesc},
					{Name: "default_branch", Type: command.String, Description: defaultBranchParamDesc},
				},
				Run: wrapMCPHandler("merge-this-session-async", handleMergeThisSessionAsync),
			})
		}
	}

	// session-job-cancel controls the *-async start tools above, which are only
	// registered under clown; without clown there is no async job to cancel, so
	// it is clown-gated too. Job status and wait are no longer spinclass's to
	// serve: an async job IS a ringmaster job (#243), inspected via ringmaster's
	// own job_status/job_read/job_wait with the id the start tool returns
	// (session-job-wait retired in #21, session-job-status in #23).
	if clown.Enabled() {
		app.AddCommand(&command.Command{
			Name:  "session-job-cancel",
			Title: "Cancel Session Job",
			Description: command.Description{
				Short: "Cancel the current worktree session's running background merge/check job (kills the pre-merge hook subprocess). No-op if nothing is running.",
			},
			Annotations: &protocol.ToolAnnotations{
				ReadOnlyHint:    protocol.BoolPtr(false),
				DestructiveHint: protocol.BoolPtr(false),
				IdempotentHint:  protocol.BoolPtr(true),
				OpenWorldHint:   protocol.BoolPtr(false),
			},
			Params: []command.Param{},
			Run:    wrapMCPHandler("session-job-cancel", handleJobCancel),
		})
	}

	if len(preMergeSkills) > 0 {
		app.AddCommand(&command.Command{
			Name:  "nothing-but-the-truth",
			Title: "Record Pre-Merge Skill Attestation",
			Description: command.Description{
				Short: buildNothingButTheTruthDescription(preMergeSkills),
			},
			Annotations: &protocol.ToolAnnotations{
				ReadOnlyHint:    protocol.BoolPtr(false),
				DestructiveHint: protocol.BoolPtr(false),
				IdempotentHint:  protocol.BoolPtr(true),
				OpenWorldHint:   protocol.BoolPtr(false),
			},
			Params: []command.Param{
				{
					Name:        "skills",
					Type:        command.Array,
					Description: "One entry per skill listed in [[pre-merge-skills]]; missing or unrecognised names fail validation; empty reasoning fails validation.",
					Required:    true,
					Items: []command.Param{
						{Name: "name", Type: command.String, Description: "Skill name exactly as listed in sweatfile [[pre-merge-skills]] (e.g. eng:code-reviewer).", Required: true},
						{Name: "used", Type: command.Bool, Description: "Whether you actually invoked the skill for the current diff.", Required: true},
						{Name: "reasoning", Type: command.String, Description: "Non-empty explanation: if used=true, what you found and addressed; if used=false, why the skill wasn't applicable.", Required: true},
					},
				},
			},
			Run: wrapMCPHandler("nothing-but-the-truth", handleNothingButTheTruth),
		})
	}

	app.AddCommand(&command.Command{
		Name:  "update-this-session-description",
		Title: "Update Session Description",
		Description: command.Description{
			Short: "Update the description of the current worktree session",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(false),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: []command.Param{
			{Name: "description", Type: command.String, Description: "New description for the session", Required: true},
		},
		Run: wrapMCPHandler("update-this-session-description", handleUpdateDescription),
	})

	app.AddCommand(&command.Command{
		Name:  "spawn-session",
		Title: "Spawn Worker Session",
		Description: command.Description{
			Short: "Spawn a detached, harness-booted worker session in a sibling repo (FDR 0006). Blocks up to the hello deadline (60s default; tune via hello-timeout) waiting for the worker's SessionStart chat hello. The brief is the worker's ONLY context: include everything it needs plus an explicit 'message me back at <your session key> via chat when done' instruction. Returns the worker's session_key (= its chat address for chat-send), worktree_path, and multiplexer id. On a hello timeout the worker's worktree and session state are left in place for inspection; clean up with `sc close`. A spawned worker MAY spawn its own workers — the former one-level restriction is gone — but note that `close-child-session` authorizes on immediate parentage only, so a worker you spawn indirectly is not yours to reap.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(false),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(false),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: spawnParamList(),
		Run:    wrapMCPHandler("spawn-session", handleSpawnSession),
	})

	app.AddCommand(&command.Command{
		Name:  "fork-session",
		Title: "Fork Detached Worker Session",
		Description: command.Description{
			Short: "Fork the CURRENT repo at this session's HEAD into a detached, harness-booted worker on a new branch (FDR 0006). Same-repo by design — the worker lands in this repo's .worktrees/<branch>; use spawn-session for a sibling repo. Same hello/timeout/cleanup contract as spawn-session: blocks up to the hello deadline (60s default; tune via hello-timeout) waiting for the worker's SessionStart chat hello. The brief is the worker's ONLY context: include everything it needs plus an explicit 'message me back at <your session key> via chat when done' instruction. Returns the worker's session_key (= its chat address for chat-send), worktree_path, and multiplexer id. On a failure after worktree creation the forked worktree is left in place for inspection (session state too when the failure came after launch, e.g. a hello timeout; bad spawn config fails before any state is written); clean up with `sc close`. A spawned worker MAY fork its own workers — the former one-level restriction is gone — but note that `close-child-session` authorizes on immediate parentage only, so a worker you spawn indirectly is not yours to reap.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(false),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(false),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: forkSessionParamList(),
		Run:    wrapMCPHandler("fork-session", handleForkSession),
	})

	app.AddCommand(&command.Command{
		Name:  "close-child-session",
		Title: "Close Spawned Child Session",
		Description: command.Description{
			Short: "Reap a worker session THIS session spawned via spawn-session or fork-session (FDR 0006, #249) — remove its worktree, delete its branch, and tombstone its state, the same teardown `sc close` performs. Authorization is the child's spawned_by lineage: a child spawned by a DIFFERENT session, or one never spawned at all, is refused with both session keys named — reaping those remains the child's own or the human's call. A child holding uncommitted changes or commits not yet integrated into the default branch is refused too; pass force to discard that work deliberately. The case this exists for — a spawn that failed its hello handshake, or a worker that finished and left nothing behind — has no commits and a clean tree, so it reaps without force.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(false),
			DestructiveHint: protocol.BoolPtr(true),
			IdempotentHint:  protocol.BoolPtr(false),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: closeChildSessionParamList(),
		Run:    wrapMCPHandler("close-child-session", handleCloseChildSession),
	})
}

// localOnlyParamDesc and defaultBranchParamDesc are shared by merge-this-session
// and its -async twin so the two surfaces cannot drift (#126/#158).
const localOnlyParamDesc = "Merge into the LOCAL default branch only — skip the pull-before and push-after. Default is to pull+push so the merge reaches origin and 'merged' means 'on origin' (#126). Set this only for a deliberate local-only merge you intend to push later; the result text flags a local-only merge as NOT pushed."

const defaultBranchParamDesc = `Override the default branch when both main and master exist in the repo. Omit to auto-detect; pass "main" or "master" to resolve the ambiguity without an interactive terminal.`

// appendNotPushedNote makes a local-only merge result say so explicitly
// (#158): spawned workers truthfully reported "merged green" from local-only
// merges while origin never got the work, and their drivers rebased onto
// origin and missed it. Since #126 push is the default, this only fires when
// the caller opted out via local_only. Worded to stay true on the
// nothing-to-merge short-circuit too (the work is local either way).
func appendNotPushedNote(text string, gitSync bool, mergeErr error) string {
	if gitSync || mergeErr != nil || text == "" {
		return text
	}
	return text + "\nNOTE: NOT pushed (local_only) — this work exists on the LOCAL default branch only; origin does not have it until someone pushes."
}

func handleMergeThisSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params struct {
		LocalOnly     bool   `json:"local_only"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	gitSync := !params.LocalOnly // push by default; local_only opts out (#126)

	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}

	gs, failMsg, ok, gitErr := resolveGatedSession(cwd)
	if !ok {
		return command.TextErrorResult(failMsg), nil
	}
	if gitErr != nil {
		// Merge treats a git-resolution failure on the worktree path as fatal.
		return command.TextErrorResult(gitErr.Error()), nil
	}

	if gs.implicit {
		// Implicit (main-checkout) session: hook-then-push, no rebase.
		var buf bytes.Buffer
		rep := crap.NewReporter(&buf, crap.ReporterOptions{Title: "merge " + gs.branch, Source: "spinclass"})
		ts := rep.TestStream(0)
		blobLinks, mergeErr := merge.MergeImplicit(context.Background(), rep, ts, gs.repoPath, cwd, gs.branch, nil)
		ts.Finish()
		text := present.RenderPlain(bytes.NewReader(buf.Bytes()))
		if mergeErr != nil && text == "" {
			text = mergeErr.Error()
		}
		return buildHookResult(text, blobLinks, mergeErr), nil
	}

	defaultBranch := params.DefaultBranch
	if defaultBranch == "" {
		var err error
		defaultBranch, err = merge.ResolveDefaultBranch(gs.repoPath)
		if err != nil {
			return command.TextErrorResult(fmt.Sprintf("could not determine default branch: %v", err)), nil
		}
	}

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{Title: "merge " + gs.branch, Source: "spinclass"})
	ts := rep.TestStream(0)
	blobLinks, mergeErr := merge.Resolved(
		executor.ShellExecutor{},
		rep,
		ts,
		gs.repoPath,
		cwd,
		gs.branch,
		defaultBranch,
		gitSync,
		true,
	)
	ts.Finish()
	text := present.RenderPlain(bytes.NewReader(buf.Bytes()))
	if mergeErr != nil && text == "" {
		text = mergeErr.Error()
	}
	text = appendNotPushedNote(text, gitSync, mergeErr)
	return buildHookResult(text, blobLinks, mergeErr), nil
}

func handleCheckThisSession(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}

	// Check needs only the gate's ok flag, not the resolved identity — it always
	// runs the hook against cwd. The discarded gitErr (a worktree git-resolution
	// failure) is deliberately ignored: check tolerates it and runs the hook
	// against cwd regardless; only an outright reject (!ok) — a genuine
	// non-session cwd or a refused gate — stops it. (sc check, the CLI, remains
	// the gate-free human escape hatch for an arbitrary dir.)
	if _, failMsg, ok, _ := resolveGatedSession(cwd); !ok {
		return command.TextErrorResult(failMsg), nil
	}

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{Title: "check", Source: "spinclass"})
	blobLinks, hookErr := check.Run(rep, cwd)
	text := present.RenderPlain(bytes.NewReader(buf.Bytes()))
	if hookErr != nil && text == "" {
		text = hookErr.Error()
	}
	return buildHookResult(text, blobLinks, hookErr), nil
}

// handleMergeThisSessionAsync starts the merge (incl. the pre-merge hook) in a
// background goroutine and returns a job id immediately, so the call is never
// subject to the client's MCP request timeout. Consumes the pre-merge
// attestation at start, exactly like the synchronous merge-this-session. The
// result is retrieved via ringmaster's own surfaces (job_status/job_read/
// job_wait) using the returned id; only registered under clown.
func handleMergeThisSessionAsync(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params struct {
		LocalOnly     bool   `json:"local_only"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	gitSync := !params.LocalOnly // push by default; local_only opts out (#126)

	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}
	// Refuse an already-running session BEFORE resolveGatedSession consumes the
	// pre-merge attestation (spinclass#265 deliverable 2). resolveGatedSession's
	// gate clears the buffered attestation as a side effect, so consuming it here
	// only to hit startSessionJob's ErrAlreadyRunning below would force a full
	// re-attestation of unchanged content on the retry — the exact papercut #265
	// fixes. MCP stdio processes one request to completion before reading the
	// next (startSessionJob flips the running slot before this handler returns),
	// so this check races nothing in practice; startSessionJob keeps the same
	// refusal as a backstop for any concurrent-dispatch corner. (Deliverable 1
	// turns this branch into an enqueue.)
	if job.IsRunning(cwd) {
		return jobAlreadyRunningResult(), nil
	}
	gs, failMsg, ok, gitErr := resolveGatedSession(cwd)
	if !ok {
		return command.TextErrorResult(failMsg), nil
	}
	if gitErr != nil {
		// Merge treats a git-resolution failure on the worktree path as fatal.
		return command.TextErrorResult(gitErr.Error()), nil
	}
	if gs.implicit {
		// Implicit (main-checkout) session: hook-then-push, no rebase. There is
		// no synchronous PrepareMerge prefix (no rebase to do up front) — run
		// MergeImplicit entirely inside the job goroutine.
		repoPath := gs.repoPath
		branch := gs.branch
		var buf bytes.Buffer
		rep := crap.NewReporter(&buf, crap.ReporterOptions{Title: "merge " + branch, Source: "spinclass"})
		ts := rep.TestStream(0)
		return startSessionJob(cwd, job.KindMerge, gitSync, func(ctx context.Context, w io.Writer) (string, bool) {
			_, mergeErr := merge.MergeImplicit(ctx, rep, ts, repoPath, cwd, branch, w)
			ts.Finish()
			text := present.RenderPlain(bytes.NewReader(buf.Bytes()))
			if mergeErr != nil && text == "" {
				text = mergeErr.Error()
			}
			return text, mergeErr != nil
		}), nil
	}
	repoPath := gs.repoPath
	branch := gs.branch
	defaultBranch := params.DefaultBranch
	if defaultBranch == "" {
		var err error
		defaultBranch, err = merge.ResolveDefaultBranch(repoPath)
		if err != nil {
			return command.TextErrorResult(fmt.Sprintf("could not determine default branch: %v", err)), nil
		}
	}

	// Run the fast prefix (optional pull + rebase + pin) synchronously, before
	// returning the job id: this frees the session worktree the moment the
	// rebase lands (the agent can edit/commit immediately), pins the exact sha
	// the backgrounded hook verifies and merges, and surfaces rebase conflicts /
	// nothing-to-merge right away instead of as an orphan job. The shared
	// reporter+buffer carry the prefix's records into FinishMerge so the final
	// job result is one continuous stream (pull → rebase → hook → merge → push).
	// Single-writer discipline: the job goroutine only writes buf after this
	// synchronous prefix returns, and nothing reads buf until the closure
	// renders it — no locking needed.
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{Title: "merge " + branch, Source: "spinclass"})
	ts := rep.TestStream(0)
	pinnedSha, prepErr := merge.PrepareMerge(ts, repoPath, cwd, branch, defaultBranch, gitSync)
	if prepErr != nil {
		ts.Finish()
		text := present.RenderPlain(bytes.NewReader(buf.Bytes()))
		if text == "" {
			text = prepErr.Error()
		}
		return buildHookResult(text, nil, prepErr), nil
	}

	return startSessionJob(cwd, job.KindMerge, gitSync, func(ctx context.Context, w io.Writer) (string, bool) {
		_, mergeErr := merge.FinishMerge(
			ctx, executor.ShellExecutor{}, rep, ts,
			repoPath, cwd, branch, defaultBranch, pinnedSha, gitSync, true, w,
		)
		ts.Finish()
		text := present.RenderPlain(bytes.NewReader(buf.Bytes()))
		if mergeErr != nil && text == "" {
			text = mergeErr.Error()
		}
		text = appendNotPushedNote(text, gitSync, mergeErr)
		return text, mergeErr != nil
	}), nil
}

// handleCheckThisSessionAsync is the non-blocking variant of
// check-this-session: it runs the pre-merge hook in the background and returns
// a job id immediately.
func handleCheckThisSessionAsync(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}
	// Refuse an already-running session before the gate consumes the attestation
	// (spinclass#265 deliverable 2), same rationale as the async merge handler.
	if job.IsRunning(cwd) {
		return jobAlreadyRunningResult(), nil
	}
	// Check needs only the gate's ok flag, not the resolved identity — it always
	// runs the hook against cwd. The discarded gitErr (a worktree git-resolution
	// failure) is deliberately ignored: check tolerates it and runs the hook
	// against cwd regardless. A cwd that is neither a worktree nor an implicit
	// session keeps the (accurate) reject; a refused gate is fatal.
	if _, failMsg, ok, _ := resolveGatedSession(cwd); !ok {
		return command.TextErrorResult(failMsg), nil
	}

	// Reporter built before startSessionJob (NewReporter writes its Meta record
	// synchronously); check.RunContext owns and finishes its own TestStream.
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{Title: "check", Source: "spinclass"})
	return startSessionJob(cwd, job.KindCheck, false, func(ctx context.Context, w io.Writer) (string, bool) {
		_, hookErr := check.RunContext(ctx, rep, cwd, w)
		text := present.RenderPlain(bytes.NewReader(buf.Bytes()))
		if hookErr != nil && text == "" {
			text = hookErr.Error()
		}
		return text, hookErr != nil
	}), nil
}

// startSessionJob launches fn as the worktree's background job and renders the
// MCP result (job started, or already-running / start error).
func startSessionJob(wt, kind string, gitSync bool, fn job.Func) *command.Result {
	id := fmt.Sprintf("%s-%d", kind, time.Now().Unix())
	j, err := job.Start(wt, kind, gitSync, id, fn)
	if err != nil {
		if errors.Is(err, job.ErrAlreadyRunning) {
			return jobAlreadyRunningResult()
		}
		return command.TextErrorResult(fmt.Sprintf("could not start background job: %v", err))
	}
	// j.ID is the ringmaster job id when running under clown (#243), so it is
	// the SAME id the completion wake reports — say so, since the whole point
	// of adopting it is that an agent can match the two.
	return command.TextResult(fmt.Sprintf(
		"started background %s job %q; the completion wake reports this same id. The pre-merge hook is running detached, so this call is not subject to the MCP request timeout. Inspect progress and the final result via ringmaster's job_status/job_read with that id, or block on it with job_wait; session-job-cancel to stop it.",
		kind, j.ID,
	))
}

// jobAlreadyRunningResult is the shared refusal for an async merge/check tool
// invoked while a background job is already in flight for the session. It is
// returned both by the pre-consume guard in the async handlers (which refuses
// WITHOUT consuming the pre-merge attestation — spinclass#265 deliverable 2:
// a refusal must never burn the scarce attestation token) and by
// startSessionJob as the backstop for the near-impossible concurrent-dispatch
// race the guard cannot see.
func jobAlreadyRunningResult() *command.Result {
	return command.TextErrorResult("a background job is already running for this session; inspect it via ringmaster (job_status/job_read/job_wait) with its id, or session-job-cancel it first")
}

// handleJobCancel cancels the worktree session's running background job.
func handleJobCancel(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}
	if !worktree.IsWorktree(cwd) {
		return command.TextErrorResult("not inside a worktree session"), nil
	}
	if job.Cancel(cwd) {
		return command.TextResult("cancel signal sent; the job tears down and emits its terminal state — confirm via ringmaster's job_status/job_read, or wait for the completion wake"), nil
	}
	return command.TextResult("no background job is currently running for this session"), nil
}

// buildMergeAsyncDescription / buildCheckAsyncDescription mirror their
// synchronous counterparts but document the non-blocking flow. The async tools
// are registered only under clown (registerMCPOnlyCommands), so the job-wakeup
// contract is always in force here — there is no clown-absent variant, and
// inspection is via ringmaster's own surfaces (job_status/job_read/job_wait).
func buildMergeAsyncDescription(hookPreview string) string {
	base := "Non-blocking variant of merge-this-session: starts the merge (including the pre-merge hook) in the background and returns a job id immediately, so the call is never cut off by the MCP request timeout no matter how long the hook runs. Consumes the pre-merge attestation at start, exactly like merge-this-session. The pre-merge hook runs in a dedicated build worktree, not yours, so the session worktree stays free — keep editing and committing there while the job runs; your concurrent edits are simply left for the next merge (never lost, never half-merged). While queued behind the per-repo merge queue (concurrent sessions' merges serialize; FDR 0022), the job log carries `merge queue: waiting behind <session>` heartbeats. This session runs under clown, so a job-wakeup notification ([clown-job] spinclass <job-id> <state>: ...) arrives when the job finishes — start the job, then make progress on other work or simply end your turn; do not poll. Your task list is the test: pending items ⇒ async; empty board ⇒ sync or async-then-end-turn. Inspect on demand with ringmaster's job_status/job_read using the returned job id (a spinclass async job IS a ringmaster job); to block on the result instead, use ringmaster's job_wait; session-job-cancel to abort."
	if hookPreview == "" {
		return base
	}
	return base + fmt.Sprintf(" The configured [hooks].pre-merge command is `%s`.", hookPreview)
}

func buildCheckAsyncDescription(hookPreview string) string {
	base := "Non-blocking variant of check-this-session: runs the [hooks].pre-merge command in the background and returns a job id immediately (never cut off by the MCP request timeout). This session runs under clown, so a job-wakeup notification ([clown-job] spinclass <job-id> <state>: ...) arrives when the job finishes — start the job, then make progress on other work or simply end your turn; do not poll. Your task list is the test: pending items ⇒ async; empty board ⇒ sync or async-then-end-turn. Inspect on demand with ringmaster's job_status/job_read using the returned job id (a spinclass async job IS a ringmaster job); to block on the result instead, use ringmaster's job_wait; session-job-cancel to abort."
	if hookPreview == "" {
		return base
	}
	return base + fmt.Sprintf(" The configured pre-merge command is `%s`.", hookPreview)
}

// gatedSession is the resolved identity of a worktree or implicit session,
// after the pre-merge attestation gate has passed. Only valid when
// resolveGatedSession returned a nil gitErr and ok=true.
type gatedSession struct {
	implicit bool   // true → main-checkout (implicit) session; false → worktree session
	repoPath string // worktree: git.CommonDir(cwd); implicit: the checkout (== cwd)
	branch   string // worktree: git.BranchCurrent(cwd); implicit: implicit.Branch
}

// resolveGatedSession resolves the session at cwd for a gated merge/check MCP
// tool and enforces the matching pre-merge attestation gate (enforceAttestation
// for a worktree session, enforceAttestationImplicit for an implicit one).
//
// Return contract — exactly one of three outcomes:
//   - reject:  ok=false, failMsg set         → caller returns command.TextErrorResult(failMsg).
//   - git-fail: ok=true, gitErr set           → worktree-path git.CommonDir/BranchCurrent
//     failed; gs is zero. The MERGE tools treat
//     this as fatal; the CHECK tools tolerate it
//     (run the hook against cwd anyway).
//   - resolved: ok=true, gitErr=nil, gs valid → proceed with gs.implicit/repoPath/branch.
//
// Separating gitErr from gs (rather than carrying it inside a partially-zero
// gatedSession with ok=true) makes the merge tools' fatality handling
// impossible to forget: a caller that ignores gitErr is visibly dropping a
// return value. gitErr is the LAST return (per staticcheck ST1008); it is
// non-nil only in the git-fail outcome (ok=true, gs zero).
func resolveGatedSession(cwd string) (gs gatedSession, failMsg string, ok bool, gitErr error) {
	if worktree.IsWorktree(cwd) {
		repoPath, repoErr := git.CommonDir(cwd)
		if repoErr != nil {
			return gatedSession{}, "", true, fmt.Errorf("could not determine repo path: %v", repoErr)
		}
		branch, branchErr := git.BranchCurrent(cwd)
		if branchErr != nil {
			return gatedSession{}, "", true, fmt.Errorf("could not determine current branch: %v", branchErr)
		}
		if msg, gok := enforceAttestation(repoPath, branch); !gok {
			return gatedSession{}, msg, false, nil
		}
		return gatedSession{repoPath: repoPath, branch: branch}, "", true, nil
	}
	implicit, _, ferr := session.FindImplicitAtCwd(cwd)
	if ferr != nil || implicit == nil {
		return gatedSession{}, "not inside a worktree session", false, nil
	}
	if msg, gok := enforceAttestationImplicit(cwd); !gok {
		return gatedSession{}, msg, false, nil
	}
	return gatedSession{implicit: true, repoPath: implicit.RepoPath, branch: implicit.Branch}, "", true, nil
}

// enforceAttestation runs the pre-merge skill attestation gate for the
// given worktree session. Returns (output, true) when the gate is
// dormant or satisfied (output discarded by the caller). Returns
// (failureText, false) when the gate refuses to proceed: the caller
// ships failureText to the agent unchanged.
//
// On internal error (e.g. session-state write failure during consume),
// returns the wrapped error message and false so the agent sees the
// concrete problem rather than a silent skip.
func enforceAttestation(repoPath, branch string) (string, bool) {
	merged, ok := mergedSweatfileForCwd()
	if !ok {
		return "", true
	}
	if len(merged.ActivePreMergeSkills()) == 0 {
		return "", true
	}
	gateOK, output, err := attestation.Check(merged, repoPath, branch)
	if err != nil && !errors.Is(err, attestation.ErrAttestationRequired) {
		return fmt.Sprintf("attestation gate error: %v", err), false
	}
	if !gateOK {
		return output, false
	}
	return "", true
}

// enforceAttestationImplicit is enforceAttestation for an implicit
// (main-checkout) session: it consults the per-randID attestation via
// CheckImplicit instead of the worktree-keyed Check. Same contract: ("",true)
// when dormant/satisfied, (failureText,false) when the gate refuses.
func enforceAttestationImplicit(checkout string) (string, bool) {
	merged, ok := mergedSweatfileForCwd()
	if !ok {
		return "", true
	}
	if len(merged.ActivePreMergeSkills()) == 0 {
		return "", true
	}
	gateOK, output, err := attestation.CheckImplicit(merged, checkout)
	if err != nil && !errors.Is(err, attestation.ErrAttestationRequired) {
		return fmt.Sprintf("attestation gate error: %v", err), false
	}
	if !gateOK {
		return output, false
	}
	return "", true
}

// buildHookResult assembles a command.Result that pairs the plain-rendered
// verdict text with one resource_link content block per blob emitted by the
// pre-merge hook. With no blob links (madder unpinned, or no hook
// configured), it falls back to the legacy text-only Result. Each link
// carries the MIME type matching the format the hook output was written
// in, so MCP-aware clients can parse/render the blob appropriately. The
// error flag is preserved either way.
func buildHookResult(text string, blobLinks []check.BlobLink, hookErr error) *command.Result {
	if len(blobLinks) == 0 {
		if hookErr != nil {
			return command.TextErrorResult(text)
		}
		return command.TextResult(text)
	}
	blocks := make([]protocol.ContentBlockV1, 0, 1+len(blobLinks))
	blocks = append(blocks, protocol.TextContentV1(text))
	for _, link := range blobLinks {
		blocks = append(blocks, protocol.ResourceLinkContent(
			link.URI,
			"pre-merge hook output",
			"Full output from the pre-merge hook, content-addressed in the per-worktree madder store.",
			link.MimeType,
		))
	}
	res := command.MultiContentResult(blocks...)
	if hookErr != nil {
		res.IsErr = true
	}
	return res
}

func handleUpdateDescription(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}

	if !worktree.IsWorktree(cwd) {
		implicit, randID, ferr := session.FindImplicitAtCwd(cwd)
		if ferr != nil || implicit == nil {
			return command.TextErrorResult("not inside a worktree session"), nil
		}
		implicit.Description = params.Description
		if err := session.WriteImplicit(*implicit, randID); err != nil {
			return command.TextErrorResult(fmt.Sprintf("could not write session state: %v", err)), nil
		}
		return command.TextResult(fmt.Sprintf("description updated to: %s", params.Description)), nil
	}

	repoPath, err := git.CommonDir(cwd)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine repo path: %v", err)), nil
	}

	branch, err := git.BranchCurrent(cwd)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine current branch: %v", err)), nil
	}

	// Auto-heal a worktree that has no spinclass state yet (an agent that ran
	// the harness directly instead of via `sc start`/`resume`): synthesize a
	// minimal record so the description sticks rather than failing on the
	// missing index (#161).
	st, err := resolveOrHealWorktreeState(repoPath, branch)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not read session state: %v", err)), nil
	}

	st.Description = params.Description
	if err := session.Write(*st); err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not write session state: %v", err)), nil
	}

	return command.TextResult(fmt.Sprintf("description updated to: %s", params.Description)), nil
}

// resolveOrHealWorktreeState reads the worktree session at (repoPath, branch),
// synthesizing minimal active state when the worktree has none yet (#161 —
// e.g. the harness ran directly in the worktree, never via sc start/resume).
// Shared by the update-this-session-description MCP handler and the
// `sc update-description` CLI so the two surfaces cannot drift (the lesson of
// #139). The session key prefers $SPINCLASS_SESSION_ID (which spinclass exports
// and which IS the key), falling back to the conventional <repo-dirname>/<branch>.
func resolveOrHealWorktreeState(repoPath, branch string) (*session.State, error) {
	key := os.Getenv("SPINCLASS_SESSION_ID")
	if key == "" {
		key = filepath.Base(repoPath) + "/" + branch
	}
	return session.EnsureWorktreeState(repoPath, branch, key, os.Getpid())
}

// lazyImplicit caches the implicit session key this serve process
// materialized lazily (see currentSessionKey, #141). The cache pins the
// identity for the process lifetime: chat sender attribution and the
// chat-read cursor key off it, and a SessionStart hook re-fire mid-session
// can add a sibling state file that FindImplicitAtCwd's first-live pick
// would otherwise flip the identity to. Keyed by cwd out of paranoia (serve
// never changes directory) and to keep tests with distinct fixtures from
// observing each other's cache.
var lazyImplicit struct {
	sync.Mutex
	cwd, key string
}

// currentSessionKey resolves the session key (<repo-dirname>/<branch>) for
// the current session. It prefers $SPINCLASS_SESSION_ID — which spinclass
// exports into every session and which IS the session key — and falls back
// to deriving it from the current worktree when the variable is unset (e.g.
// the tool is exercised by hand outside a managed session). When cwd is not a
// worktree it falls back further to a live implicit (main-checkout) session's
// state file (see the inline comment for the shared-checkout caveat), and
// when no live implicit session exists it materializes one lazily (#141).
// Returns an error only when none of the four sources resolves.
func currentSessionKey() (string, error) {
	if v := os.Getenv("SPINCLASS_SESSION_ID"); v != "" {
		return v, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not get working directory: %w", err)
	}
	if !worktree.IsWorktree(cwd) {
		lazyImplicit.Lock()
		defer lazyImplicit.Unlock()
		if lazyImplicit.key != "" && lazyImplicit.cwd == cwd {
			return lazyImplicit.key, nil
		}
		// Implicit (main-checkout) session: resolve the key from the live
		// per-session state file. Note: when multiple agents share one main
		// checkout, this returns the first live implicit session's key — the
		// serve process can't know its own Claude session_id, so it can't
		// disambiguate further. Chat broadcasts still reach every session
		// regardless; directed sends from such a shared checkout may resolve
		// to a sibling's key. (See #118 — acceptable for v1.)
		if st, _, ferr := session.FindImplicitAtCwd(cwd); ferr == nil && st != nil {
			return st.SessionKey, nil
		}
		// No live implicit session — the SessionStart hook never fired (or
		// its gates failed). Materialize one lazily so chat and spawn sender
		// resolution work from a session-less main checkout (#141), behind
		// the same gates as the hook (incl. [hooks].disable-implicit-sessions).
		// The randID is process-random — serve cannot know the Claude
		// session_id the hook derives its rand from — and the recorded PID is
		// this serve process, so liveness tracks the session exactly and the
		// dead-PID sweep reaps the file after the session ends (SessionEnd
		// removes only the hook-derived randID, never this one).
		if key, ok := hooks.MaterializeImplicit(cwd, lazyImplicitRand(), os.Getpid()); ok {
			lazyImplicit.cwd, lazyImplicit.key = cwd, key
			return key, nil
		}
		return "", errors.New(
			"no live implicit session at this checkout, $SPINCLASS_SESSION_ID is unset, " +
				"and lazy materialization is gated (cwd must be a main-checkout repo root " +
				"on a branch; check [hooks].disable-implicit-sessions)",
		)
	}
	repoPath, err := git.CommonDir(cwd)
	if err != nil {
		return "", fmt.Errorf("could not determine repo path: %w", err)
	}
	branch, err := git.BranchCurrent(cwd)
	if err != nil {
		return "", fmt.Errorf("could not determine current branch: %w", err)
	}
	st, err := session.Read(repoPath, branch)
	if errors.Is(err, os.ErrNotExist) {
		// Untracked worktree (the harness ran here directly, never via
		// sc start/resume, so shop.Attach wrote no state): lazily register
		// it so chat/spawn resolve a stable, listable, addressable identity
		// rather than failing on the missing index — the worktree sibling of
		// #141's implicit lazy materialization (#163). Persisting (vs.
		// resolving in-memory) is deliberate and matches #141: a sender that
		// isn't on disk can't be a chat-list-sessions entry or a reply target.
		// Write failure is non-fatal — the key still resolves, so chat-send
		// from here degrades to "send works, not addressable" rather than a
		// hard error.
		if synth, herr := resolveOrHealWorktreeState(repoPath, branch); herr == nil {
			_ = session.Write(*synth)
			return synth.SessionKey, nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("could not read session state: %w", err)
	}
	return st.SessionKey, nil
}

// lazyImplicitRand returns a fresh random rand-suffix for a lazily
// materialized implicit session — same 16-hex shape as the SessionStart
// hook's sha256(session_id)[:8].
func lazyImplicitRand() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b)
}

func handleNothingButTheTruth(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params struct {
		Skills []session.AttestedSkill `json:"skills"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}

	isImplicit := false
	var repoPath, branch string
	if worktree.IsWorktree(cwd) {
		repoPath, err = git.CommonDir(cwd)
		if err != nil {
			return command.TextErrorResult(fmt.Sprintf("could not determine repo path: %v", err)), nil
		}
		branch, err = git.BranchCurrent(cwd)
		if err != nil {
			return command.TextErrorResult(fmt.Sprintf("could not determine current branch: %v", err)), nil
		}
	} else {
		implicit, _, ferr := session.FindImplicitAtCwd(cwd)
		if ferr != nil || implicit == nil {
			return command.TextErrorResult("not inside a worktree session"), nil
		}
		isImplicit = true
	}

	merged, ok := mergedSweatfileForCwd()
	if !ok {
		return command.TextErrorResult("could not resolve sweatfile hierarchy"), nil
	}
	required := merged.ActivePreMergeSkills()
	if len(required) == 0 {
		return command.TextErrorResult("nothing-but-the-truth is unavailable: this repo's sweatfile does not declare [[pre-merge-skills]]"), nil
	}

	verr := attestation.Validate(required, params.Skills)
	if !verr.Empty() {
		return command.TextErrorResult(renderValidationError(required, verr)), nil
	}

	if isImplicit {
		err = attestation.RecordImplicit(cwd, params.Skills)
	} else {
		err = attestation.Record(repoPath, branch, params.Skills)
	}
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not record attestation: %v", err)), nil
	}

	return command.TextResult(fmt.Sprintf("ok - attestation recorded for %d skill(s); call merge-this-session or check-this-session to consume it", len(required))), nil
}

// renderValidationError formats a ValidationError as a plain-text error body
// that names the required list and the offending entries so the agent
// can correct and retry without re-fetching the skill list.
func renderValidationError(required []sweatfile.PreMergeSkill, verr attestation.ValidationError) string {
	var b strings.Builder
	b.WriteString("attestation rejected: " + verr.Error() + "\n\n")
	b.WriteString("required skills (sweatfile [[pre-merge-skills]]):\n")
	for _, s := range required {
		fmt.Fprintf(&b, "  - %s: %s\n", s.Name, s.Rationale)
	}
	return b.String()
}

// buildNothingButTheTruthDescription writes the tool's description with
// the resolved [[pre-merge-skills]] list inlined so an agent reading the
// catalog sees the required names and rationales without a separate
// fetch.
func buildNothingButTheTruthDescription(skills []sweatfile.PreMergeSkill) string {
	var b strings.Builder
	b.WriteString("Record a pre-merge skill attestation. This repo's sweatfile requires you to address every skill listed below — one entry per skill, with `used` indicating whether you invoked it on the current diff and `reasoning` explaining your decision. Strict on presence (every name must be addressed), lenient on content (any non-empty reasoning is accepted). Each attestation is consumed by the next merge-this-session or check-this-session call.\n\n")
	b.WriteString("Required skills:\n")
	for _, s := range skills {
		fmt.Fprintf(&b, "- %s — %s\n", s.Name, s.Rationale)
	}
	return strings.TrimRight(b.String(), "\n")
}

// buildMergeThisSessionDescription assembles the merge-this-session MCP
// tool description, optionally appending the resolved [hooks].pre-merge
// command so agents know what tests/checks the merge will run before
// invoking it (and skip redundant pre-flight runs of the same suite).
func buildMergeThisSessionDescription(hookPreview string) string {
	base := "Merge the current session's worktree into the default branch and clean up. Pushes to origin by default (#126); pass local_only to keep the merge on the LOCAL default branch (the result then flags it NOT pushed). A non-error return means the merge (and push, unless local_only) succeeded; the result text is plain verdict lines (✓/✗ per stage) and is informational — it does not need to be read or parsed to confirm success. This blocks until the pre-merge hook and merge finish (including any wait on the per-repo merge queue — concurrent sessions' merges serialize; FDR 0022), then returns — the right choice when you have nothing else to do (no polling needed). Reach for merge-this-session-async only when you have other independent work to make progress on while the hook runs. When madder is pinned at build time, the response also carries a real MCP `resource_link` content block (URI scheme `madder://blobs/<digest>`) pointing to the full pre-merge hook output. MCP-aware agents fetch via `resources/read`; inspect only on failure."
	if hookPreview == "" {
		return base
	}
	return base + fmt.Sprintf(
		" The configured [hooks].pre-merge command runs as part of this tool: `%s`. Agents do not need to pre-flight that command before calling merge-this-session.",
		hookPreview,
	)
}

// buildCheckThisSessionDescription assembles the check-this-session MCP
// tool description, optionally appending the resolved [hooks].pre-merge
// command. Same rationale as buildMergeThisSessionDescription: callers
// should know which command executes so they don't shadow it.
func buildCheckThisSessionDescription(hookPreview string) string {
	base := "Run the configured [hooks].pre-merge command in the current worktree without merging. This is the agent-CI surface; safe to call repeatedly. Returns non-zero / error if the hook fails. This blocks until the hook finishes, then returns — the right choice when you have nothing else to do (no polling needed). Reach for check-this-session-async only when you have other independent work to make progress on while the hook runs. When madder is pinned at build time, the response is compact: plain verdict lines (✓/✗) plus a real MCP `resource_link` content block (URI scheme `madder://blobs/<digest>`) pointing to the full output. MCP-aware agents fetch via `resources/read`; inspect only on failure."
	if hookPreview == "" {
		return base
	}
	return base + fmt.Sprintf(" The configured pre-merge command is `%s`.", hookPreview)
}

// mergedSweatfileForCwd loads the sweatfile hierarchy applicable to the
// current working directory and returns the merged result. Returns the
// zero Sweatfile and false on any error so callers can degrade gracefully
// (e.g. a misconfigured environment must not strip the merge tool, and
// it must not crash MCP startup).
func mergedSweatfileForCwd() (sweatfile.Sweatfile, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return sweatfile.Sweatfile{}, false
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return sweatfile.Sweatfile{}, false
	}
	repoPath, err := git.CommonDir(cwd)
	if err != nil {
		// Not a worktree (or not a git repo): load the simple hierarchy.
		h, hErr := sweatfileio.LoadHierarchy(home, cwd)
		if hErr != nil {
			return sweatfile.Sweatfile{}, false
		}
		return h.Merged, true
	}
	h, err := sweatfileio.LoadWorktreeHierarchy(home, repoPath, cwd)
	if err != nil {
		return sweatfile.Sweatfile{}, false
	}
	return h.Merged, true
}

// mergeDisabledForCwd reports whether [hooks].disable-merge is set in the
// merged sweatfile applicable to the current working directory. Returns
// false on any load error so a misconfigured environment doesn't silently
// strip the merge tool.
func mergeDisabledForCwd() bool {
	merged, ok := mergedSweatfileForCwd()
	if !ok {
		return false
	}
	return merged.DisableMergeEnabled()
}

// preMergeSkillsForCwd returns the resolved [[pre-merge-skills]] list
// for the current working directory's merged sweatfile, filtered to
// active entries (non-empty rationale). Returns nil on any load error
// so a misconfigured environment doesn't silently register the
// attestation tool.
func preMergeSkillsForCwd() []sweatfile.PreMergeSkill {
	merged, ok := mergedSweatfileForCwd()
	if !ok {
		return nil
	}
	return merged.ActivePreMergeSkills()
}

// preMergeHookForCwd returns the resolved [hooks].pre-merge command for
// the current working directory's merged sweatfile, or "" when no hook is
// configured (or the hierarchy can't be loaded). Multi-line scripts are
// reduced to their first non-empty line followed by " ..." so the value
// fits in an MCP tool description.
func preMergeHookForCwd() string {
	merged, ok := mergedSweatfileForCwd()
	if !ok {
		return ""
	}
	cmd := merged.PreMergeHookCommand()
	if cmd == nil || *cmd == "" {
		return ""
	}
	return summarizeHookCommand(*cmd)
}

// summarizeHookCommand returns a single-line preview of a hook script
// suitable for inclusion in an MCP tool description. Multi-line scripts
// are reduced to the first non-empty line plus a " ..." suffix.
func summarizeHookCommand(script string) string {
	var first string
	var seenAfter bool
	for _, line := range bytes.Split([]byte(script), []byte{'\n'}) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if first != "" {
				continue
			}
			continue
		}
		if first == "" {
			first = string(trimmed)
			continue
		}
		seenAfter = true
		break
	}
	if first == "" {
		return ""
	}
	if seenAfter {
		return first + " ..."
	}
	return first
}
