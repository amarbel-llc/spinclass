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
	// noSubcommand marks a tool whose single verb has no `subcommand` argument.
	noSubcommand bool
}

func gritParam(name string, typ command.ParamType, desc string) command.Param {
	return command.Param{Name: name, Type: typ, Description: desc}
}

var (
	gritRepoPath = gritParam("repo_path", command.String, "Path to the git repository (defaults to the session worktree; almost never needed).")
	gritPaths    = gritParam("paths", command.Array, "File paths to limit the operation to.")
	gritCtx      = gritParam("context_lines", command.Int, "Context lines around each change, git --unified=N.")
	gritStat     = gritParam("stat_only", command.Bool, "Show only a diffstat summary.")
	gritForce    = gritParam("force", command.Bool, "Force the operation (stage rm: git rm --force; branch delete: git branch -D). Always asks for confirmation.")
)

func gritTools() []gritTool {
	return []gritTool{
		{
			name:  "stage",
			short: "Inspect and mutate the git index / working tree. Subcommands: status, diff, add, unstage, rm, mv.",
			extra: []command.Param{
				gritPaths, gritCtx, gritStat, gritRepoPath, gritForce,
				gritParam("staged", command.Bool, "Diff only staged changes, git diff --cached (diff only)."),
				gritParam("untracked", command.String, "Untracked file mode (status only): no, normal (default) or all."),
				gritParam("ignored", command.Bool, "Also show ignored files (status only)."),
				gritParam("intent_to_add", command.Bool, "Record only the intent to add paths, git add -N (add only)."),
				gritParam("recursive", command.Bool, "Allow recursive removal of directories (rm only)."),
				gritParam("source", command.String, "Source path (mv only)."),
				gritParam("destination", command.String, "Destination path (mv only)."),
			},
		},
		{
			name:         "commit",
			short:        "Create a new commit from the staged changes, using the identity git itself resolves. fixup and squash are mutually exclusive and make message ignored.",
			noSubcommand: true,
			extra: []command.Param{
				gritRepoPath,
				gritParam("message", command.String, "Commit message. Required unless amend (reuses the old message when omitted), fixup or squash is set."),
				gritParam("amend", command.Bool, "Amend the previous commit instead of creating a new one."),
				gritParam("allow_empty", command.Bool, "Allow a commit that records no changes, --allow-empty."),
				gritParam("fixup", command.String, "Create a fixup! commit targeting this commit, --fixup=<commit>."),
				gritParam("squash", command.String, "Create a squash! commit targeting this commit, --squash=<commit>."),
			},
		},
		{
			name:  "branch",
			short: "Inspect and mutate branches and history. Subcommands: log, rev-parse, diff, create, delete, reset-soft, reset-hard, checkout, rebase, restack, cherry-pick, revert. Destructive operations on the default branch are refused.",
			extra: []command.Param{
				gritPaths, gritCtx, gritStat, gritRepoPath, gritForce,
				gritParam("ref", command.String, "Target ref. log: start ref (default HEAD). rev-parse: the ref to resolve (required). diff: the ref to diff against. reset-soft: where to move HEAD (default HEAD~1). reset-hard: where to move HEAD (required). checkout: branch/ref to switch to, the new branch's name with new_branch, or the ref to restore paths from."),
				gritParam("max_count", command.Int, "Maximum number of commits to show, git -n (log only)."),
				gritParam("oneline", command.Bool, "Compact one-line log format (log only)."),
				gritParam("name", command.String, "Branch name (create, delete)."),
				gritParam("start_point", command.String, "Starting point for the new branch: commit, branch or tag (create, checkout with new_branch; default HEAD)."),
				gritParam("new_branch", command.Bool, "Create the branch named by ref and check it out, git checkout -b (checkout only)."),
				gritParam("ours", command.Bool, "During a merge conflict restore our version of paths (checkout with paths only)."),
				gritParam("theirs", command.Bool, "During a merge conflict restore their version of paths (checkout with paths only)."),
				gritParam("commits", command.Array, "Commit hashes to apply, in order (cherry-pick and revert, required)."),
				gritParam("no_commit", command.Bool, "Apply changes without creating commits (cherry-pick and revert only)."),
				gritParam("upstream", command.String, "Ref to rebase onto (rebase only, required unless continuing, aborting or skipping)."),
				gritParam("onto", command.String, "New base to graft onto, git rebase --onto. rebase: optional. restack: required."),
				gritParam("rebase_branch", command.String, "Branch to rebase (rebase only; defaults to the current branch)."),
				gritParam("autosquash", command.Bool, "Squash fixup!/squash! commits into their targets, non-interactively (rebase only)."),
				gritParam("autostash", command.Bool, "Stash and unstash uncommitted changes around the rebase (rebase only)."),
				gritParam("update_refs", command.Bool, "Reposition intermediate branch refs touched by the rebase (rebase only)."),
				gritParam("continue", command.Bool, "Continue an in-progress rebase after resolving conflicts (rebase only)."),
				gritParam("abort", command.Bool, "Abort an in-progress rebase (rebase only)."),
				gritParam("skip", command.Bool, "Skip the current commit and continue the rebase (rebase only)."),
				gritParam("root", command.String, "Inclusive lower bound of the stack to restack, typically the bottom branch (restack only, required)."),
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
// 3: reads plus owned-branch writes; clone/merge/remote/stash are later steps).
func registerGritCommands(app *command.App) {
	for _, t := range gritTools() {
		name := "grit_" + t.name
		params := t.extra
		if !t.noSubcommand {
			params = append([]command.Param{{
				Name:        "subcommand",
				Type:        command.String,
				Required:    true,
				Description: "The " + t.name + " operation to run.",
			}}, t.extra...)
		}
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
	if head.Subcommand == "" && tool != "commit" {
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
