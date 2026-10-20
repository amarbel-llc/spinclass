package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/store"
	gitstore "code.linenisgreat.com/spinclass/internal/store/git"
)

// gritBackend is the store backend behind the grit_* tools (FDR 0036). The
// sweatfile `[store] backend` selector is a later step; git is the only one.
var gritBackend store.Backend = gitstore.New()

// gritTool describes one grit_<tool> MCP tool. Leaf names stay byte-identical
// to moxy's grit tools (FDR 0036 section 6).
type gritTool struct {
	name  string
	short string
	extra []command.Param
}

func gritParam(name string, typ command.ParamType, desc string) command.Param {
	return command.Param{Name: name, Type: typ, Description: desc}
}

var (
	gritRepoPath = gritParam("repo_path", command.String, "Path to the git repository (defaults to the session worktree; almost never needed).")
	gritPaths    = gritParam("paths", command.Array, "File paths to limit the operation to.")
	gritCtx      = gritParam("context_lines", command.Int, "Context lines around each change, git --unified=N.")
	gritStat     = gritParam("stat_only", command.Bool, "Show only a diffstat summary.")
)

func gritTools() []gritTool {
	return []gritTool{
		{
			name:  "stage",
			short: "Inspect the git index / working tree. Supported subcommands in spinclass so far: status, diff (the write subcommands add, unstage, rm, mv are not yet supported).",
			extra: []command.Param{
				gritPaths, gritCtx, gritStat, gritRepoPath,
				gritParam("staged", command.Bool, "Diff only staged changes, git diff --cached (diff only)."),
				gritParam("untracked", command.String, "Untracked file mode (status only): no, normal (default) or all."),
				gritParam("ignored", command.Bool, "Also show ignored files (status only)."),
			},
		},
		{
			name:  "branch",
			short: "Inspect branches and history. Supported subcommands in spinclass so far: log, rev-parse, diff (create, delete, reset-*, checkout, rebase, restack, cherry-pick, revert are not yet supported).",
			extra: []command.Param{
				gritPaths, gritCtx, gritStat, gritRepoPath,
				gritParam("ref", command.String, "Target ref. log: start ref (default HEAD). rev-parse: the ref to resolve (required). diff: the ref to diff against."),
				gritParam("max_count", command.Int, "Maximum number of commits to show, git -n (log only)."),
				gritParam("oneline", command.Bool, "Compact one-line log format (log only)."),
			},
		},
		{
			name:  "tag",
			short: "Inspect git tags. Supported subcommands in spinclass so far: list, verify (create, delete, push are not yet supported).",
			extra: []command.Param{
				gritRepoPath,
				gritParam("name", command.String, "Tag name (verify)."),
				gritParam("pattern", command.String, "Glob to filter tag names (list only)."),
				gritParam("contains", command.String, "Only show tags reachable from this commit/ref (list only)."),
				gritParam("sort", command.String, "Sort key passed to git tag --sort (list only; default -creatordate)."),
				gritParam("max_count", command.Int, "Cap the number of tags returned (list only; 0 means no cap)."),
			},
		},
		{
			name:  "worktree",
			short: "Inspect git worktrees. Supported subcommands in spinclass so far: list (remove is not yet supported).",
			extra: []command.Param{gritRepoPath},
		},
	}
}

// registerGritCommands registers the grit_<tool> store tools (FDR 0036, step
// 1: read subcommands only; clone/commit/merge/remote/stash are later steps).
func registerGritCommands(app *command.App) {
	for _, t := range gritTools() {
		name := "grit_" + t.name
		params := append([]command.Param{{
			Name:        "subcommand",
			Type:        command.String,
			Required:    true,
			Description: "The " + t.name + " operation to run.",
		}}, t.extra...)
		app.AddCommand(&command.Command{
			Name:        name,
			Title:       "Grit " + t.name,
			Description: command.Description{Short: t.short},
			Annotations: &protocol.ToolAnnotations{
				ReadOnlyHint:    protocol.BoolPtr(false),
				DestructiveHint: protocol.BoolPtr(false),
				IdempotentHint:  protocol.BoolPtr(false),
				OpenWorldHint:   protocol.BoolPtr(false),
			},
			Params: params,
			Run:    wrapMCPHandler(name, gritHandler(t.name)),
		})
	}
}

func gritHandler(tool string) func(context.Context, json.RawMessage, command.Prompter) (*command.Result, error) {
	return func(ctx context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
		text, err := runGrit(ctx, tool, args)
		if err != nil {
			return command.TextErrorResult(err.Error()), nil
		}
		return command.TextResult(text), nil
	}
}

func runGrit(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	var head struct {
		Subcommand string `json:"subcommand"`
	}
	if err := json.Unmarshal(args, &head); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if head.Subcommand == "" {
		return "", errors.New("subcommand is required")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not get working directory: %w", err)
	}
	s := store.Session{Dir: cwd}
	if branch, berr := git.BranchCurrent(cwd); berr == nil {
		s.Branch = branch
	}
	res, err := gritBackend.Do(ctx, s, store.Op{Tool: tool, Subcommand: head.Subcommand, Args: args})
	if errors.Is(err, store.ErrUnsupported) {
		return "", fmt.Errorf("grit_%s %s is not yet supported in spinclass (FDR 0036); supported: %v", tool, head.Subcommand, gritBackend.Capabilities()[tool])
	}
	if err != nil {
		return "", err
	}
	return res.Text, nil
}
