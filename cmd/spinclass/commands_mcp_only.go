package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/protocol"
	"github.com/amarbel-llc/spinclass/internal/attestation"
	"github.com/amarbel-llc/spinclass/internal/chat"
	"github.com/amarbel-llc/spinclass/internal/check"
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
				Short: buildMergeAsyncDescription(hookPreview),
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
			Short: "Poll the current worktree session's background merge/check job (started by a *-this-session-async tool): reports running|succeeded|failed|cancelled|interrupted, elapsed, last-activity, a tail of live hook output, and the full result when finished.",
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
			Short: "Post a message to the global cross-session chatroom. Omit `to` (or pass \"*\") to broadcast to every session; pass a session key (the `<repo>/<branch>` shown in `sc list`, == another session's $SPINCLASS_SESSION_ID) to direct-message one session. Receiving sessions are pushed new messages by the chat-watch monitor; no read call is needed on their side.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(false),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(false),
			OpenWorldHint:   protocol.BoolPtr(true),
		},
		Params: []command.Param{
			{Name: "message", Type: command.String, Description: "Message body to send", Required: true},
			{Name: "to", Type: command.String, Description: "Recipient session key (<repo>/<branch>); omit or \"*\" to broadcast"},
		},
		Run: wrapMCPHandler("chat-send", handleChatSend),
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
	return startSessionJob(cwd, job.KindMerge, gitSync, func(ctx context.Context, w io.Writer) (string, bool) {
		var buf bytes.Buffer
		_, mergeErr := merge.ResolvedContext(
			ctx, executor.ShellExecutor{}, &buf, nil, "tap",
			repoPath, cwd, branch, defaultBranch, gitSync, true, true, w,
		)
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

	switch j.Status {
	case job.StatusRunning:
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
	case job.StatusInterrupted:
		return command.TextErrorResult(fmt.Sprintf(
			"job %q (%s) was interrupted: the serve process ended mid-run, so the merge/check was cut off. Start a new one.",
			j.ID, j.Kind,
		)), nil
	default:
		header := fmt.Sprintf("job %q (%s): %s, elapsed %s\n", j.ID, j.Kind, j.Status, j.Elapsed().Round(time.Second))
		if j.ResultIsErr || j.Status != job.StatusSucceeded {
			return command.TextErrorResult(header + j.ResultText), nil
		}
		return command.TextResult(header + j.ResultText), nil
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
// synchronous counterparts but document the non-blocking start+poll flow.
func buildMergeAsyncDescription(hookPreview string) string {
	base := "Non-blocking variant of merge-this-session: starts the merge (including the pre-merge hook) in the background and returns a job id immediately, so the call is never cut off by the MCP request timeout no matter how long the hook runs. Consumes the pre-merge attestation at start, exactly like merge-this-session. Poll session-job-status for progress and the final result; session-job-cancel to abort."
	if hookPreview == "" {
		return base
	}
	return base + fmt.Sprintf(" The configured [hooks].pre-merge command is `%s`.", hookPreview)
}

func buildCheckAsyncDescription(hookPreview string) string {
	base := "Non-blocking variant of check-this-session: runs the [hooks].pre-merge command in the background and returns a job id immediately (never cut off by the MCP request timeout). Poll session-job-status for progress and the result; session-job-cancel to abort."
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

func handleChatSend(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	var params struct {
		Message string `json:"message"`
		To      string `json:"to"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	if params.Message == "" {
		return command.TextErrorResult("message is required"), nil
	}

	from, err := currentSessionKey()
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}

	to := params.To
	if to == "" {
		to = chat.Broadcast
	}
	if err := chat.Send(chat.Message{From: from, To: to, Body: params.Message}); err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not send message: %v", err)), nil
	}

	dest := "all sessions"
	if to != chat.Broadcast {
		dest = to
	}
	return command.TextResult(fmt.Sprintf("sent to %s (from %s)", dest, from)), nil
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
	base := "Merge the current session's worktree into the default branch and clean up. A non-error return means the merge (and push, if git_sync) succeeded; the output payload is informational and does not need to be read or parsed to confirm success. When madder is pinned at build time, the response also carries a real MCP `resource_link` content block (URI scheme `madder://blobs/<digest>`) pointing to the full pre-merge hook output. MCP-aware agents fetch via `resources/read`; inspect only on failure."
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
	base := "Run the configured [hooks].pre-merge command in the current worktree without merging. This is the agent-CI surface; safe to call repeatedly. Returns non-zero / error if the hook fails. When madder is pinned at build time, the response is compact: a single test point per hook step plus a real MCP `resource_link` content block (URI scheme `madder://blobs/<digest>`) pointing to the full output. MCP-aware agents fetch via `resources/read`; inspect only on failure."
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
