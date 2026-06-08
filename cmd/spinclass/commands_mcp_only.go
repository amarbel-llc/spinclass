package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/protocol"
	"github.com/amarbel-llc/spinclass/internal/attestation"
	"github.com/amarbel-llc/spinclass/internal/chat"
	"github.com/amarbel-llc/spinclass/internal/check"
	"github.com/amarbel-llc/spinclass/internal/clown"
	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/job"
	"github.com/amarbel-llc/spinclass/internal/merge"
	"github.com/amarbel-llc/spinclass/internal/servelog"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/worktree"
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
		app.AddCommand(&command.Command{
			Name:  "check-this-session-async",
			Title: "Check This Session (async)",
			Description: command.Description{
				Short: buildCheckAsyncDescription(hookPreview, clown.Enabled()),
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
				{Name: "git_sync", Type: command.Bool, Description: "Pull and push after merge (default false)"},
			},
			Run: wrapMCPHandler("merge-this-session", handleMergeThisSession),
		})
		app.AddCommand(&command.Command{
			Name:  "merge-this-session-async",
			Title: "Merge This Session (async)",
			Description: command.Description{
				Short: buildMergeAsyncDescription(hookPreview, clown.Enabled()),
			},
			Annotations: &protocol.ToolAnnotations{
				ReadOnlyHint:    protocol.BoolPtr(false),
				DestructiveHint: protocol.BoolPtr(true),
				IdempotentHint:  protocol.BoolPtr(false),
				OpenWorldHint:   protocol.BoolPtr(false),
			},
			Params: []command.Param{
				{Name: "git_sync", Type: command.Bool, Description: "Pull and push after merge (default false)"},
			},
			Run: wrapMCPHandler("merge-this-session-async", handleMergeThisSessionAsync),
		})
	}

	// Job poll/cancel tools are always available — they control the *-async
	// start tools above. status is read-only; cancel is idempotent.
	app.AddCommand(&command.Command{
		Name:  "session-job-status",
		Title: "Session Job Status",
		Description: command.Description{
			Short: "Poll the current worktree session's background merge/check job (started by a *-this-session-async tool): reports running|succeeded|failed|cancelled|interrupted, elapsed, last-activity, a tail of live hook output, and the full result when finished. Poll sparingly — only check back after making progress on other work. Do NOT spin in a tight loop waiting on a job with nothing else to do; if that's your situation you should have started the merge/check with the synchronous tool, which blocks and returns the result for you.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: []command.Param{},
		Run:    wrapMCPHandler("session-job-status", handleJobStatus),
	})
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
	app.AddCommand(&command.Command{
		Name:  "session-job-wait",
		Title: "Wait For Session Job",
		Description: command.Description{
			Short: "Block until the current worktree session's background merge/check job (started by a *-this-session-async tool) finishes, then return its full result — the same payload the synchronous merge-this-session / check-this-session would have produced. Use this to revert to synchronous behaviour: start async because you had other work, then call session-job-wait once that work is done instead of hot-polling session-job-status. Returns immediately if the job has already finished; errors if no job has been started. NOTE: this blocks, so it is subject to the MCP request timeout for the job's *remaining* duration — call it when the job is at or near completion, not right after starting a long one.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: []command.Param{},
		Run:    wrapMCPHandler("session-job-wait", handleJobWait),
	})

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
		Name:  "chat-send",
		Title: "Send Cross-Session Chat Message",
		Description: command.Description{
			Short: "Post a message to the global cross-session chatroom. `subject` is a one-line summary (max 200 chars) — it is ALL the recipient's push notification carries, so make it stand alone; put detail in `body`, which recipients recover via chat-read. Omit `to` (or pass \"*\") to broadcast to every session; pass a session key (the `<repo>/<branch>` shown in `sc list`, == another session's $SPINCLASS_SESSION_ID) to direct-message one session. Receiving sessions are pushed new messages by clown's job-watch monitor; no read call is needed on their side. The chatroom records and displays each message's sender automatically, so do NOT include or announce your own session ID — write only the content.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(false),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(false),
			OpenWorldHint:   protocol.BoolPtr(true),
		},
		Params: []command.Param{
			{Name: "subject", Type: command.String, Description: "One-line summary (max 200 chars) — the only part carried in the recipient's push notification. Required unless body/message is given (then derived from its first line)."},
			{Name: "body", Type: command.String, Description: "Full message content, any length; recipients recover it via chat-read"},
			{Name: "message", Type: command.String, Description: "DEPRECATED alias for body (subject is derived from its first line)"},
			{Name: "to", Type: command.String, Description: "Recipient session key (<repo>/<branch>); omit or \"*\" to broadcast"},
		},
		Run: wrapMCPHandler("chat-send", handleChatSend),
	})

	app.AddCommand(&command.Command{
		Name:  "chat-read",
		Title: "Read Cross-Session Chat Messages",
		Description: command.Description{
			Short: "Read cross-session chat messages new since this session last read. Defaults to the full cross-session firehose (every message from every session); narrow with the optional filters. Advances this session's read cursor unless `peek` is true. This is the pull counterpart to the clown job-watch push (and the only receive path without clown, e.g. macOS or bare spinclass).",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(false),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(false),
			OpenWorldHint:   protocol.BoolPtr(true),
		},
		Params: []command.Param{
			{Name: "to_me", Type: command.Bool, Description: "Only broadcasts and direct messages addressed to this session"},
			{Name: "from", Type: command.String, Description: "Only messages from this sender session key (<repo>/<branch>)"},
			{Name: "repo", Type: command.String, Description: "Only messages from senders in this repo (the <repo> segment of the sender key)"},
			{Name: "peek", Type: command.Bool, Description: "Preview without advancing the read cursor (a later read still returns these messages)"},
		},
		Run: wrapMCPHandler("chat-read", handleChatRead),
	})

	app.AddCommand(&command.Command{
		Name:  "chat-list-sessions",
		Title: "List Active Chat Sessions",
		Description: command.Description{
			Short: "List active (non-abandoned) spinclass sessions across all repos — the candidate recipients for a directed chat-send. Each row is a session key (<repo>/<branch>), its repo, state, and description. Optionally filter to one repo.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(true),
		},
		Params: []command.Param{
			{Name: "repo", Type: command.String, Description: "Only sessions in this repo (repo directory name)"},
		},
		Run: wrapMCPHandler("chat-list-sessions", handleChatListSessions),
	})
}

func handleMergeThisSession(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params struct {
		GitSync bool `json:"git_sync"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}

	if !worktree.IsWorktree(cwd) {
		return command.TextErrorResult("not inside a worktree session"), nil
	}

	repoPath, err := git.CommonDir(cwd)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine repo path: %v", err)), nil
	}

	branch, err := git.BranchCurrent(cwd)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine current branch: %v", err)), nil
	}

	if msg, ok := enforceAttestation(repoPath, branch); !ok {
		return command.TextErrorResult(msg), nil
	}

	defaultBranch, err := merge.ResolveDefaultBranch(repoPath)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine default branch: %v", err)), nil
	}

	var buf bytes.Buffer
	blobLinks, mergeErr := merge.Resolved(
		executor.ShellExecutor{},
		&buf,
		nil,
		"tap",
		repoPath,
		cwd,
		branch,
		defaultBranch,
		params.GitSync,
		true,
		true,
	)
	return buildHookResult(buf.String(), blobLinks, mergeErr), nil
}

func handleCheckThisSession(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}

	if worktree.IsWorktree(cwd) {
		repoPath, repoErr := git.CommonDir(cwd)
		branch, branchErr := git.BranchCurrent(cwd)
		if repoErr == nil && branchErr == nil {
			if msg, ok := enforceAttestation(repoPath, branch); !ok {
				return command.TextErrorResult(msg), nil
			}
		}
	}

	var buf bytes.Buffer
	blobLinks, hookErr := check.Run(&buf, "tap", cwd, false)
	text := buf.String()
	if hookErr != nil && text == "" {
		text = hookErr.Error()
	}
	return buildHookResult(text, blobLinks, hookErr), nil
}

// handleMergeThisSessionAsync starts the merge (incl. the pre-merge hook) in a
// background goroutine and returns a job id immediately, so the call is never
// subject to the client's MCP request timeout. Consumes the pre-merge
// attestation at start, exactly like the synchronous merge-this-session. The
// result is retrieved via session-job-status.
func handleMergeThisSessionAsync(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params struct {
		GitSync bool `json:"git_sync"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}
	if !worktree.IsWorktree(cwd) {
		return command.TextErrorResult("not inside a worktree session"), nil
	}
	repoPath, err := git.CommonDir(cwd)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine repo path: %v", err)), nil
	}
	branch, err := git.BranchCurrent(cwd)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine current branch: %v", err)), nil
	}
	if msg, ok := enforceAttestation(repoPath, branch); !ok {
		return command.TextErrorResult(msg), nil
	}
	defaultBranch, err := merge.ResolveDefaultBranch(repoPath)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine default branch: %v", err)), nil
	}

	gitSync := params.GitSync

	// Run the fast prefix (optional pull + rebase + pin) synchronously, before
	// returning the job id: this frees the session worktree the moment the
	// rebase lands (the agent can edit/commit immediately), pins the exact sha
	// the backgrounded hook verifies and merges, and surfaces rebase conflicts /
	// nothing-to-merge right away instead of as an orphan job. The shared
	// writer+buffer carry the prefix's TAP into FinishMerge so the final job
	// result is one continuous stream (pull → rebase → hook → merge → push).
	var buf bytes.Buffer
	tw := merge.NewMergeWriter(&buf)
	pinnedSha, prepErr := merge.PrepareMerge(tw, &buf, repoPath, cwd, branch, defaultBranch, gitSync, true)
	if prepErr != nil {
		tw.Plan()
		return buildHookResult(buf.String(), nil, prepErr), nil
	}

	return startSessionJob(cwd, job.KindMerge, gitSync, func(ctx context.Context, w io.Writer) (string, bool) {
		_, mergeErr := merge.FinishMerge(
			ctx, executor.ShellExecutor{}, tw, &buf,
			repoPath, cwd, branch, defaultBranch, pinnedSha, gitSync, true, true, w,
		)
		tw.Plan()
		return buf.String(), mergeErr != nil
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
	if !worktree.IsWorktree(cwd) {
		return command.TextErrorResult("not inside a worktree session"), nil
	}
	repoPath, repoErr := git.CommonDir(cwd)
	branch, branchErr := git.BranchCurrent(cwd)
	if repoErr == nil && branchErr == nil {
		if msg, ok := enforceAttestation(repoPath, branch); !ok {
			return command.TextErrorResult(msg), nil
		}
	}

	return startSessionJob(cwd, job.KindCheck, false, func(ctx context.Context, w io.Writer) (string, bool) {
		var buf bytes.Buffer
		_, hookErr := check.RunContext(ctx, &buf, "tap", cwd, false, w)
		text := buf.String()
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
			return command.TextErrorResult("a background job is already running for this session; poll session-job-status, or session-job-cancel it first")
		}
		return command.TextErrorResult(fmt.Sprintf("could not start background job: %v", err))
	}
	return command.TextResult(fmt.Sprintf(
		"started background %s job %q; the pre-merge hook is running detached, so this call is not subject to the MCP request timeout. Poll session-job-status for progress and the final result; session-job-cancel to stop it.",
		kind, j.ID,
	))
}

// handleJobStatus reports the worktree session's background job.
func handleJobStatus(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}
	if !worktree.IsWorktree(cwd) {
		return command.TextErrorResult("not inside a worktree session"), nil
	}
	j, err := job.Read(cwd)
	if errors.Is(err, os.ErrNotExist) {
		return command.TextResult("no background job has been started for this session"), nil
	}
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not read job state: %v", err)), nil
	}

	if j.Status == job.StatusRunning {
		lastDesc := "n/a"
		if last := job.LastActivity(cwd); !last.IsZero() {
			lastDesc = time.Since(last).Round(time.Second).String() + " ago"
		}
		body := fmt.Sprintf("job %q (%s): running, elapsed %s, last activity %s",
			j.ID, j.Kind, j.Elapsed().Round(time.Second), lastDesc)
		if tail := job.TailLog(cwd, 15); tail != "" {
			body += "\n--- recent hook output ---\n" + tail
		}
		return command.TextResult(body), nil
	}
	return renderFinishedJob(j), nil
}

// renderFinishedJob renders a terminal (non-running) job as the MCP result,
// mirroring the synchronous tool: a header line plus the stored result payload,
// surfaced as an error result on failure / interruption. Shared by
// session-job-status and session-job-wait.
func renderFinishedJob(j *job.Job) *command.Result {
	if j.Status == job.StatusInterrupted {
		return command.TextErrorResult(fmt.Sprintf(
			"job %q (%s) was interrupted: the serve process ended mid-run, so the merge/check was cut off. Start a new one.",
			j.ID, j.Kind,
		))
	}
	header := fmt.Sprintf("job %q (%s): %s, elapsed %s\n", j.ID, j.Kind, j.Status, j.Elapsed().Round(time.Second))
	if j.ResultIsErr || j.Status != job.StatusSucceeded {
		return command.TextErrorResult(header + j.ResultText)
	}
	return command.TextResult(header + j.ResultText)
}

// handleJobWait blocks until the worktree session's background job finishes and
// returns its result (join semantics — it never starts a job). An agent that
// went async because it had other work calls this once that work is done,
// instead of hot-polling session-job-status.
func handleJobWait(ctx context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}
	if !worktree.IsWorktree(cwd) {
		return command.TextErrorResult("not inside a worktree session"), nil
	}
	for {
		j, err := job.Read(cwd)
		if errors.Is(err, os.ErrNotExist) {
			return command.TextErrorResult(
				"no background job to wait on for this session; start one with merge-this-session-async / check-this-session-async, or use the synchronous merge-this-session / check-this-session",
			), nil
		}
		if err != nil {
			return command.TextErrorResult(fmt.Sprintf("could not read job state: %v", err)), nil
		}
		if j.Status != job.StatusRunning {
			return renderFinishedJob(j), nil
		}
		select {
		case <-ctx.Done():
			return command.TextErrorResult(fmt.Sprintf(
				"wait cancelled before the job finished (%v); the job is still running — poll session-job-status or session-job-cancel it",
				ctx.Err(),
			)), nil
		case <-job.WaitDone(cwd):
			// Job finished (or wasn't tracked in this serve process); loop to
			// re-read the terminal record.
		}
	}
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
		return command.TextResult("cancel signal sent; poll session-job-status to confirm the job stopped"), nil
	}
	return command.TextResult("no background job is currently running for this session"), nil
}

// buildMergeAsyncDescription / buildCheckAsyncDescription mirror their
// synchronous counterparts but document the non-blocking flow. clownWake
// (clown.Enabled() at serve startup) selects the guidance: with the
// job-wakeup channel a completion notification wakes the agent, so the
// poll-discipline warnings are replaced by the wake contract.
func buildMergeAsyncDescription(hookPreview string, clownWake bool) string {
	var base string
	if clownWake {
		base = "Non-blocking variant of merge-this-session: starts the merge (including the pre-merge hook) in the background and returns a job id immediately, so the call is never cut off by the MCP request timeout no matter how long the hook runs. Consumes the pre-merge attestation at start, exactly like merge-this-session. This session runs under clown, so a job-wakeup notification ([clown-job] spinclass <job-id> <state>: ...) arrives when the job finishes — start the job, then make progress on other work or simply end your turn; do not poll. Your task list is the test: pending items ⇒ async; empty board ⇒ sync or async-then-end-turn. session-job-status remains available for on-demand inspection, session-job-wait to block, session-job-cancel to abort."
	} else {
		base = "Non-blocking variant of merge-this-session: starts the merge (including the pre-merge hook) in the background and returns a job id immediately, so the call is never cut off by the MCP request timeout no matter how long the hook runs. Consumes the pre-merge attestation at start, exactly like merge-this-session. Poll session-job-status for progress and the final result; session-job-cancel to abort. Choose this ONLY when you have other independent work to make progress on while the hook runs — then check back via session-job-status occasionally. If you have nothing else to do, call the synchronous merge-this-session instead: it blocks and returns the result with no polling. Do NOT pick async and then spin in a tight session-job-status loop — that wastes turns for no benefit over the synchronous tool."
	}
	if hookPreview == "" {
		return base
	}
	return base + fmt.Sprintf(" The configured [hooks].pre-merge command is `%s`.", hookPreview)
}

func buildCheckAsyncDescription(hookPreview string, clownWake bool) string {
	var base string
	if clownWake {
		base = "Non-blocking variant of check-this-session: runs the [hooks].pre-merge command in the background and returns a job id immediately (never cut off by the MCP request timeout). This session runs under clown, so a job-wakeup notification ([clown-job] spinclass <job-id> <state>: ...) arrives when the job finishes — start the job, then make progress on other work or simply end your turn; do not poll. Your task list is the test: pending items ⇒ async; empty board ⇒ sync or async-then-end-turn. session-job-status remains available for on-demand inspection, session-job-wait to block, session-job-cancel to abort."
	} else {
		base = "Non-blocking variant of check-this-session: runs the [hooks].pre-merge command in the background and returns a job id immediately (never cut off by the MCP request timeout). Poll session-job-status for progress and the result; session-job-cancel to abort. Choose this ONLY when you have other independent work to make progress on while the hook runs — then check back via session-job-status occasionally. If you have nothing else to do, call the synchronous check-this-session instead: it blocks and returns the result with no polling. Do NOT pick async and then spin in a tight session-job-status loop."
	}
	if hookPreview == "" {
		return base
	}
	return base + fmt.Sprintf(" The configured pre-merge command is `%s`.", hookPreview)
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

// buildHookResult assembles a command.Result that pairs the rendered TAP
// text with one resource_link content block per blob emitted by the
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
		return command.TextErrorResult("not inside a worktree session"), nil
	}

	repoPath, err := git.CommonDir(cwd)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine repo path: %v", err)), nil
	}

	branch, err := git.BranchCurrent(cwd)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine current branch: %v", err)), nil
	}

	st, err := session.Read(repoPath, branch)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not read session state: %v", err)), nil
	}

	st.Description = params.Description
	if err := session.Write(*st); err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not write session state: %v", err)), nil
	}

	return command.TextResult(fmt.Sprintf("description updated to: %s", params.Description)), nil
}

func handleChatSend(ctx context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
		Message string `json:"message"` // deprecated alias for body
		To      string `json:"to"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	body := params.Body
	if body == "" {
		body = params.Message
	}
	if params.Subject == "" && body == "" {
		return command.TextErrorResult("subject (and optionally body) is required"), nil
	}
	// Explicit subjects are validated strictly; a subject derived from the
	// body (alias path) is clipped instead — see chat.DisplaySubject.
	if err := chat.ValidateSubject(params.Subject); err != nil {
		return command.TextErrorResult(err.Error()), nil
	}

	from, err := currentSessionKey()
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}

	to := params.To
	if to == "" {
		to = chat.Broadcast
	}
	msg := chat.Message{From: from, To: to, Subject: params.Subject, Body: body}
	if err := chat.Send(msg); err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not send message: %v", err)), nil
	}

	dest := "all sessions"
	if to != chat.Broadcast {
		dest = to
	}
	// The store write above is the message; the wake emit is only the push.
	// An emit failure is surfaced but must not fail the send (the recipient
	// still gets the message via chat-read / the legacy monitor).
	if err := chat.EmitWake(ctx, msg); err != nil {
		return command.TextResult(fmt.Sprintf("sent to %s (from %s); wake emit failed: %v", dest, from, err)), nil
	}
	return command.TextResult(fmt.Sprintf("sent to %s (from %s)", dest, from)), nil
}

func handleChatRead(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params struct {
		ToMe bool   `json:"to_me"`
		From string `json:"from"`
		Repo string `json:"repo"`
		Peek bool   `json:"peek"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	me, err := currentSessionKey()
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}

	msgs, err := chat.Read(me, chat.ReadFilter{ToMe: params.ToMe, From: params.From, Repo: params.Repo}, params.Peek)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not read messages: %v", err)), nil
	}
	if len(msgs) == 0 {
		return command.TextResult("no new messages"), nil
	}

	var b strings.Builder
	for _, m := range msgs {
		dest := ""
		if m.To != chat.Broadcast {
			dest = " -> " + m.To
		}
		fmt.Fprintf(&b, "from %s%s @%s: %s\n",
			m.From, dest, m.Timestamp.UTC().Format(time.RFC3339), m.DisplaySubject())
		// Full body below the header when it carries more than the
		// subject line — chat-read is the recovery surface for bodies
		// the push notification could not carry (#103).
		if m.HasMoreThanSubject() {
			fmt.Fprintf(&b, "%s\n", m.Body)
		}
	}
	return command.TextResult(strings.TrimRight(b.String(), "\n")), nil
}

func handleChatListSessions(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params struct {
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	all, err := session.ListAll(nil)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not list sessions: %v", err)), nil
	}

	live := all[:0:0]
	for _, s := range all {
		if s.ResolveState() == session.StateAbandoned {
			continue
		}
		if params.Repo != "" && filepath.Base(s.RepoPath) != params.Repo {
			continue
		}
		live = append(live, s)
	}
	session.SortStates(live)

	if len(live) == 0 {
		return command.TextResult("no active sessions"), nil
	}

	var b strings.Builder
	for _, s := range live {
		desc := ""
		if s.Description != "" {
			desc = " — " + s.Description
		}
		fmt.Fprintf(&b, "%s [%s] (%s)%s\n",
			s.SessionKey, s.ResolveState(), filepath.Base(s.RepoPath), desc)
	}
	return command.TextResult(strings.TrimRight(b.String(), "\n")), nil
}

// currentSessionKey resolves the session key (<repo-dirname>/<branch>) for
// the current session. It prefers $SPINCLASS_SESSION_ID — which spinclass
// exports into every session and which IS the session key — and falls back
// to deriving it from the current worktree when the variable is unset (e.g.
// the tool is exercised by hand outside a managed session). Returns an error
// only when neither source resolves.
func currentSessionKey() (string, error) {
	if v := os.Getenv("SPINCLASS_SESSION_ID"); v != "" {
		return v, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not get working directory: %w", err)
	}
	if !worktree.IsWorktree(cwd) {
		return "", errors.New("not inside a worktree session (and $SPINCLASS_SESSION_ID is unset)")
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
	if err != nil {
		return "", fmt.Errorf("could not read session state: %w", err)
	}
	return st.SessionKey, nil
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

	if !worktree.IsWorktree(cwd) {
		return command.TextErrorResult("not inside a worktree session"), nil
	}

	repoPath, err := git.CommonDir(cwd)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine repo path: %v", err)), nil
	}

	branch, err := git.BranchCurrent(cwd)
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not determine current branch: %v", err)), nil
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

	if err := attestation.Record(repoPath, branch, params.Skills); err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not record attestation: %v", err)), nil
	}

	return command.TextResult(fmt.Sprintf("ok - attestation recorded for %d skill(s); call merge-this-session or check-this-session to consume it", len(required))), nil
}

// renderValidationError formats a ValidationError as a TAP error body
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
	base := "Merge the current session's worktree into the default branch and clean up. A non-error return means the merge (and push, if git_sync) succeeded; the output payload is informational and does not need to be read or parsed to confirm success. This blocks until the pre-merge hook and merge finish, then returns — the right choice when you have nothing else to do (no polling needed). Reach for merge-this-session-async only when you have other independent work to make progress on while the hook runs. When madder is pinned at build time, the response also carries a real MCP `resource_link` content block (URI scheme `madder://blobs/<digest>`) pointing to the full pre-merge hook output. MCP-aware agents fetch via `resources/read`; inspect only on failure."
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
	base := "Run the configured [hooks].pre-merge command in the current worktree without merging. This is the agent-CI surface; safe to call repeatedly. Returns non-zero / error if the hook fails. This blocks until the hook finishes, then returns — the right choice when you have nothing else to do (no polling needed). Reach for check-this-session-async only when you have other independent work to make progress on while the hook runs. When madder is pinned at build time, the response is compact: a single test point per hook step plus a real MCP `resource_link` content block (URI scheme `madder://blobs/<digest>`) pointing to the full output. MCP-aware agents fetch via `resources/read`; inspect only on failure."
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
		h, hErr := sweatfile.LoadHierarchy(home, cwd)
		if hErr != nil {
			return sweatfile.Sweatfile{}, false
		}
		return h.Merged, true
	}
	h, err := sweatfile.LoadWorktreeHierarchy(home, repoPath, cwd)
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
