package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/spinclass/internal/auth"
	"code.linenisgreat.com/spinclass/internal/confinement"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/store"
	gitstore "code.linenisgreat.com/spinclass/internal/store/git"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
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
			short:        "Create a new commit from the staged changes, using the sweatfile [store] identity when set, else the identity git itself resolves. fixup and squash are mutually exclusive and make message ignored.",
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
			short: "Inspect and mutate branches and history. Subcommands: log, rev-parse, diff, create, delete, reset-soft, reset-hard, checkout, rebase, restack, cherry-pick, revert. Destructive operations on the default branch are refused. Accepted forms: create takes name (the new branch) and optional start_point; checkout of an existing branch takes ref; checkout with new_branch takes name (the new branch) and optional start_point (the legacy form, ref alone naming the new branch, works only for a plain local-style name); delete takes the short branch name or refs/heads/<name>, any other refs/ prefix is refused. A new branch is never named like a remote-tracking ref (origin/master) or with a refs/ prefix; to branch from origin/master pass name=<new> start_point=origin/master.",
			extra: []command.Param{
				gritPaths, gritCtx, gritStat, gritRepoPath, gritForce,
				gritParam("ref", command.String, "Target ref. log: start ref (default HEAD). rev-parse: the ref to resolve (required). diff: the ref to diff against. reset-soft: where to move HEAD (default HEAD~1). reset-hard: where to move HEAD (required). checkout: the existing branch/ref to switch to, or the ref to restore paths from; with new_branch use name and start_point instead (legacy: ref alone names the new branch if it is a plain local-style name)."),
				gritParam("max_count", command.Int, "Maximum number of commits to show, git -n (log only)."),
				gritParam("oneline", command.Bool, "Compact one-line log format (log only)."),
				gritParam("name", command.String, "Branch name. create: the new branch (short name). checkout with new_branch: the new branch (short name, never remote-tracking-shaped like origin/master). delete: the short branch name, or refs/heads/<name>; other refs/ prefixes are refused."),
				gritParam("start_point", command.String, "Starting point for the new branch: commit, branch, remote-tracking ref or tag (create, checkout with new_branch; default HEAD)."),
				gritParam("new_branch", command.Bool, "Create the branch named by name (legacy: ref) and check it out, git checkout -b (checkout only)."),
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
			name:  "stash",
			short: "Shelve and restore working changes in this worktree's own stash shelf (git's shared stash stack is never used, so no other session can see or pop these). Subcommands: save, list, apply, drop. Entries die with the worktree; closing it is refused while any remain.",
			extra: []command.Param{
				gritRepoPath,
				gritPaths,
				gritParam("message", command.String, "Stash message (save only)."),
				gritParam("include_untracked", command.Bool, "Also shelve untracked files (save only)."),
				gritParam("stash_ref", command.String, "Entry to apply or drop: stash@{N} (Nth newest) or the entry number from list. Defaults to the newest."),
			},
		},
		{
			name:  "tag",
			short: "Inspect and mutate git tags. Subcommands: list, verify, create, delete, push. A session owns tags named <session branch>--*. create signs by default; the signature needs a key this session may not have (git's error is returned), so pass sign=false or lightweight=true to opt out. Pushes never use the ambient ssh-agent.",
			extra: []command.Param{
				gritRepoPath, gritForce,
				gritParam("name", command.String, "Tag name (verify, create, delete, push)."),
				gritParam("ref", command.String, "Commit to tag (create only; default HEAD)."),
				gritParam("message", command.String, "Tag message (create only; required unless lightweight)."),
				gritParam("sign", command.Bool, "Sign the tag, git tag --sign (create only; default true unless lightweight). false makes an annotated, unsigned tag."),
				gritParam("lightweight", command.Bool, "Create a lightweight tag with no message or signature (create only)."),
				gritParam("remote", command.String, "A configured remote name (push only; default origin)."),
				gritParam("delete", command.Bool, "Delete the tag on the remote instead of pushing it (push only). Always asks."),
				gritParam("pattern", command.String, "Glob to filter tag names (list only)."),
				gritParam("contains", command.String, "Only show tags reachable from this commit/ref (list only)."),
				gritParam("sort", command.String, "Sort key passed to git tag --sort (list only; default -creatordate)."),
				gritParam("max_count", command.Int, "Cap the number of tags returned (list only; 0 means no cap)."),
			},
		},
		{
			name:  "worktree",
			short: "Inspect and remove git worktrees. Subcommands: list, remove. remove never removes the worktree the call runs in, and is auto-allowed only for another worktree whose branch the session owns.",
			extra: []command.Param{
				gritRepoPath, gritForce,
				gritParam("path", command.String, "Worktree to remove (remove only)."),
			},
		},
		{
			name:  "remote",
			short: "Talk to a configured remote. Subcommands: fetch, pull, push, push-stack. Runs in the session worktree with its own credentials and never the ambient ssh-agent; a session with no minted credential gets git's own auth error. Pushing or deleting the default branch is refused for a confined caller and always asks otherwise. push-stack force-pushes branches bottom to top with --force-with-lease after a dry run of all of them and returns a JSON {phase, results} report; it never touches the default branch.",
			extra: []command.Param{
				gritRepoPath, gritForce,
				gritParam("remote", command.String, "A configured remote name (default origin). URLs are refused."),
				gritParam("branch", command.String, "pull: remote branch to pull. push: local branch to push (default the current branch)."),
				gritParam("branches", command.Array, "Branches to push, ordered bottom to top (push-stack only)."),
				gritParam("remote_branch", command.String, "Destination branch on the remote (push only; default the local branch's name)."),
				gritParam("set_upstream", command.Bool, "Set the upstream tracking reference (push only)."),
				gritParam("force_with_lease", command.Bool, "Force-push only if the remote ref is where this repository last saw it (push only)."),
				gritParam("lease_ref_sha", command.String, "With force_with_lease: the commit the remote branch is expected to be at, the explicit --force-with-lease=<branch>:<sha> form that needs no remote-tracking ref (push only)."),
				gritParam("force_if_includes", command.Bool, "Also require the pushed commit to include the remote tip, --force-if-includes (push only; needs force_with_lease)."),
				gritParam("delete", command.Bool, "Delete the remote branch named by branch or remote_branch (push only). Always asks."),
				gritParam("all", command.Bool, "Fetch from all remotes (fetch only)."),
				gritParam("prune", command.Bool, "Prune remote-tracking branches that no longer exist on the remote (fetch only)."),
				gritParam("tags", command.Bool, "Fetch all tags (fetch only)."),
				gritParam("rebase", command.Bool, "Rebase instead of merge (pull only)."),
			},
		},
		{
			name:         "merge",
			short:        "Merge a branch into the current branch. Refused on the default branch. Auto-allowed only when the current branch is owned by the session.",
			noSubcommand: true,
			extra: []command.Param{
				gritRepoPath,
				gritParam("branch", command.String, "Branch to merge. Required unless abort or continue is set."),
				gritParam("no_ff", command.Bool, "Create a merge commit even if a fast-forward is possible."),
				gritParam("squash", command.Bool, "Squash the branch's commits into the working tree without committing."),
				gritParam("abort", command.Bool, "Abort an in-progress merge."),
				gritParam("continue", command.Bool, "Continue a merge after resolving conflicts."),
			},
		},
		{
			name:         "clone",
			short:        "Clone a repository into destination. Auto-allowed only into the session worktree (its .tmp included); anywhere else asks. A confined caller may not clone. The source is not gated; the clone never uses the ambient ssh-agent.",
			noSubcommand: true,
			extra: []command.Param{
				gritParam("source", command.String, "Repository to clone: a local path or URL."),
				gritParam("destination", command.String, "Target directory, relative to the session worktree."),
				gritParam("branch", command.String, "Branch or tag to check out, --branch."),
				gritParam("depth", command.Int, "Create a shallow clone with this many commits, --depth."),
				gritParam("bare", command.Bool, "Create a bare repository, --bare."),
			},
		},
	}
}

// gritToolsWithoutSubcommand are the tools whose single verb has no
// `subcommand` argument.
var gritToolsWithoutSubcommand = map[string]bool{"commit": true, "merge": true, "clone": true}

// registerGritCommands registers the grit_<tool> store tools (FDR 0036): all
// nine of moxy's grit tools.
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
			return command.TextErrorResult(nonEmptyText(err.Error(), "grit: the operation failed with no message")), nil
		}
		return command.TextResult(nonEmptyText(text, gritNoOutput)), nil
	}
}

// gritNoOutput stands in for the empty output of a git verb that succeeds
// silently (stage add, branch checkout). The V1 content block omits an empty
// `text`, which a client rejects as "content[0] missing text".
const gritNoOutput = "ok (git printed no output)"

func nonEmptyText(text, fallback string) string {
	if strings.TrimSpace(text) == "" {
		return fallback
	}
	return text
}

// gritCaller resolves the calling session for a grit tool body (FDR 0036
// section 9). It is a variable so tests can stand in for the frontend state.
var gritCaller = resolveGritCaller

// registerServeRecord writes this serve's record under the state directory.
func registerServeRecord() (func(), error) {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return func() {}, err
	}
	return confinement.RegisterServe(stateDir, os.Getenv("CLOWN_SESSION_ID"), os.Getpid(), os.Getppid(), time.Now())
}

// resolveGritCaller classifies the caller by the principal of the clown hosting
// this serve, which is what the launching clown wrote into a confined unit's
// record. Unresolved counts as confined. A confined caller works in the
// worktree the frontend principal mapping names, falling back to the cwd. A
// caller whose worktree is no recognised session worktree owns nothing
// (store.Session.NoSession): its branch is not read from the cwd, and the body
// refuses every write.
func resolveGritCaller(cwd string) store.Session {
	s := store.Session{Dir: cwd, Confinement: store.ConfinementConfined}
	principal := os.Getenv("CLOWN_SESSION_ID")
	stateDir, err := confinement.DefaultStateDir()
	if err == nil {
		confined, _ := confinement.ClassifyServeCaller(confinement.Host{}, stateDir, principal, os.Getpid())
		if !confined {
			s.Confinement = store.ConfinementUnconfined
		} else if worktree, ok := confinement.WorktreeOfPrincipal(confinement.Host{}, stateDir, principal); ok {
			s.Dir = worktree
		}
	}
	if worktree, branch, ok := recognisedSessionWorktree(s.Dir); ok {
		s.Dir, s.Branch = worktree, branch
		s.Repo = trustedRepoFor(worktree)
	} else {
		s.NoSession = true
	}
	s.Identity = storeIdentityFor(s.Dir)
	return s
}

// recognisedSessionWorktree reports the worktree root and session branch when
// dir lies in a live, non-implicit spinclass session worktree.
func recognisedSessionWorktree(dir string) (worktree, branch string, ok bool) {
	top, err := git.Run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", false
	}
	st, err := session.FindByWorktreePath(top)
	if err != nil || st.IsTombstone() || st.Kind == session.KindImplicit || st.WorktreePath == "" || st.Branch == "" {
		return "", "", false
	}
	return st.WorktreePath, st.Branch, true
}

// trustedRepoFor computes the session's repository layout from the recorded
// session state (the main checkout's RepoPath and the worktree's name), never
// from the worktree's .git pointer file, which a confined unit could rewrite.
// A layout that does not check out on disk yields the zero value, which leaves
// git's own discovery (with the config neutralisers still applied).
func trustedRepoFor(worktree string) store.Repo {
	st, err := session.FindByWorktreePath(worktree)
	if err != nil || st.RepoPath == "" {
		return store.Repo{}
	}
	common := filepath.Join(st.RepoPath, ".git")
	gitDir := filepath.Join(common, "worktrees", filepath.Base(worktree))
	for _, d := range []string{common, gitDir} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			return store.Repo{}
		}
	}
	repo := store.Repo{CommonDir: common, GitDir: gitDir}
	if auth.Minted(worktree) {
		repo.CredentialFile = auth.CredentialPath(worktree)
	}
	return repo
}

// storeIdentityFor reads the [store] identity from the sweatfile layers above
// the repo applicable to dir; unreadable configuration leaves git's own
// resolution.
func storeIdentityFor(dir string) store.Identity {
	h, home, repoRoot, ok := sweatfileHierarchyForDir(dir)
	if !ok {
		return store.Identity{}
	}
	authorName, authorEmail, committerName, committerEmail, _ := sweatfileio.TrustedStoreIdentity(h, home, repoRoot)
	return store.Identity{AuthorName: authorName, AuthorEmail: authorEmail, CommitterName: committerName, CommitterEmail: committerEmail}
}

// gritSubcommand reads the subcommand out of a tool's JSON arguments and checks
// the tool needs one.
func gritSubcommand(tool string, args json.RawMessage) (string, error) {
	var head struct {
		Subcommand string `json:"subcommand"`
	}
	if err := json.Unmarshal(args, &head); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if head.Subcommand == "" && !gritToolsWithoutSubcommand[tool] {
		return "", errors.New("subcommand is required")
	}
	return head.Subcommand, nil
}

func runGrit(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	if _, err := gritSubcommand(tool, args); err != nil {
		return "", err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not get working directory: %w", err)
	}
	return runGritAs(ctx, tool, args, gritCaller(cwd))
}

// runGritAs runs one grit tool for an already-resolved caller. The MCP path and
// the `sc grit` escape hatch share it, so both reach the same backend Do.
func runGritAs(ctx context.Context, tool string, args json.RawMessage, s store.Session) (string, error) {
	subcommand, err := gritSubcommand(tool, args)
	if err != nil {
		return "", err
	}
	res, err := gritBackend.Do(ctx, s, store.Op{Tool: tool, Subcommand: subcommand, Args: args})
	if errors.Is(err, store.ErrUnsupported) {
		return "", fmt.Errorf("grit_%s %s is not yet supported in spinclass (FDR 0036); supported: %v", tool, subcommand, gritBackend.Capabilities()[tool])
	}
	if err != nil {
		return "", err
	}
	return res.Text, nil
}
