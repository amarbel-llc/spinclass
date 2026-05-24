package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"time"

	"github.com/amarbel-llc/purse-first/libs/go-mcp/command"
	"github.com/amarbel-llc/purse-first/libs/go-mcp/protocol"
	"github.com/amarbel-llc/spinclass/internal/check"
	"github.com/amarbel-llc/spinclass/internal/executor"
	"github.com/amarbel-llc/spinclass/internal/git"
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

	var buf bytes.Buffer
	blobLinks, hookErr := check.Run(&buf, "tap", cwd, false)
	text := buf.String()
	if hookErr != nil && text == "" {
		text = hookErr.Error()
	}
	return buildHookResult(text, blobLinks, hookErr), nil
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
