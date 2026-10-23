package git

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/store"
)

// refuseUnownedForConfined is the body-side ownership check for confined
// callers (FDR 0036 section 9.4). A confined worker cannot have been shown a
// permission prompt, so the tool body refuses whatever Owned rejects. It is the
// same check Classify applies to a write verb, minus the force flag, which only
// decides whether a human is asked. Unconfined callers are never checked here:
// for them the PreToolUse hook is the sole gate (section 2).
func (b Backend) refuseUnownedForConfined(s store.Session, op store.Op) error {
	if !s.Confinement.Confined() {
		return nil
	}
	if store.ClassOf(op.Tool, op.Subcommand) != store.ClassWrite {
		return refuseReadOutsideSession(s, op)
	}
	if op.Tool == "clone" {
		return fmt.Errorf("refusing clone: a confined caller may not clone; it has no place to put a clone the frontend would write")
	}
	args, ok := decodeClassifyArgs(op.Args)
	if !ok || !args.optionFree() || !b.writeAllowed(s, op, args) {
		return fmt.Errorf("refusing %s: a confined caller may only touch refs and the worktree its session owns (the session branch and %s%s*)", verbLabel(op), s.Branch, ownershipSeparator)
	}
	return nil
}

// refuseReadOutsideSession scopes a confined caller's reads: a repo_path whose
// git toplevel is not the session worktree would let the frontend read any
// repository it can reach on the worker's behalf. Fails closed on an
// unreadable repo_path, a path git cannot resolve, and a missing session
// worktree.
func refuseReadOutsideSession(s store.Session, op store.Op) error {
	args, ok := decodeClassifyArgs(op.Args)
	if !ok {
		return fmt.Errorf("refusing %s: arguments could not be read", verbLabel(op))
	}
	repoPath, ok := args.str("repo_path")
	if !ok || s.Dir == "" {
		return fmt.Errorf("refusing %s: a confined caller may only read its session worktree", verbLabel(op))
	}
	if repoPath == "" {
		return nil
	}
	if !filepath.IsAbs(repoPath) {
		repoPath = filepath.Join(s.Dir, repoPath)
	}
	top, err := git.Run(repoPath, "rev-parse", "--show-toplevel")
	if err != nil || !sameDir(top, s.Dir) {
		return fmt.Errorf("refusing %s: a confined caller may only read its session worktree", verbLabel(op))
	}
	return nil
}

// refuseWithoutSession refuses every write for a caller whose worktree is not a
// recognised spinclass session worktree (FDR 0036 section 5): it owns nothing,
// so there is no namespace to check against. Reads are unaffected.
func refuseWithoutSession(s store.Session, op store.Op) error {
	if !s.NoSession || store.ClassOf(op.Tool, op.Subcommand) == store.ClassRead {
		return nil
	}
	return fmt.Errorf("refusing %s: this worktree is not a recognised spinclass session worktree, so the caller owns nothing and every write is refused; start a session with `sc start` and run the tool from its worktree", verbLabel(op))
}

// refuseOtherSessionWorktree refuses, for every caller, a call whose dir lies in
// ANOTHER spinclass session's worktree unless the caller holds a handle on that
// session (FDR 0032). A session worktree is writable by the worker it hosts, so
// git run there on someone else's behalf can be steered by what the worker
// planted (a commondir, a config); a serve built before the commondir pin runs
// such a planted hook outright. The worktree is found from the filesystem alone
// (symlinks resolved, then the .git pointer's layout, git.LinkedWorktree), so
// nothing runs in it before the check. A dir that cannot be resolved, or a .git
// pointer of unknown layout, is refused. Plain repositories and the main
// checkout stay open to unconfined callers as before.
func refuseOtherSessionWorktree(s store.Session, op store.Op, dir string) error {
	if s.SessionAt == nil {
		return nil
	}
	path := dir
	if !filepath.IsAbs(path) && s.Dir != "" {
		path = filepath.Join(s.Dir, path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("refusing %s: cannot resolve repo_path %q (%v), so cannot tell whose worktree it is", verbLabel(op), dir, err)
	}
	root, _, linked := git.LinkedWorktree(real)
	if root != "" && !linked {
		return fmt.Errorf("refusing %s: cannot resolve the worktree layout of repo_path %q (its .git pointer names no <common>/worktrees/<name>)", verbLabel(op), dir)
	}
	if !linked {
		return nil
	}
	key, ok := s.SessionAt(root)
	if !ok || (s.Dir != "" && sameDir(root, s.Dir)) || slices.Contains(s.Held, key) {
		return nil
	}
	held := "none"
	if len(s.Held) > 0 {
		held = strings.Join(s.Held, ", ")
	}
	return fmt.Errorf("refusing %s: repo_path %s is in session %s's worktree, and this caller holds no handle on that session (handles held: %s); act on another session only through a handle on it (grant-session-handle)", verbLabel(op), dir, key, held)
}

const stashSessionLocalRefusal = "stashes are session-local: grit_stash only touches this session's own shelf (refusing repo_path outside this session's worktree)"

// refuseStashOutsideSession makes the shelf session-local for every caller
// (spinclass#395): the shelf worktree is always the session's own, and a
// repo_path whose git toplevel is anything else, or that git cannot resolve, is
// refused whether or not the caller is confined.
func refuseStashOutsideSession(s store.Session, op store.Op, a args) error {
	if op.Tool != "stash" {
		return nil
	}
	if s.Dir == "" {
		return fmt.Errorf("%s", stashSessionLocalRefusal)
	}
	if a.RepoPath == "" {
		return nil
	}
	repoPath := a.RepoPath
	if !filepath.IsAbs(repoPath) {
		repoPath = filepath.Join(s.Dir, repoPath)
	}
	top, err := git.Run(repoPath, "rev-parse", "--show-toplevel")
	if err != nil || !sameDir(top, s.Dir) {
		return fmt.Errorf("%s", stashSessionLocalRefusal)
	}
	return nil
}

func verbLabel(op store.Op) string {
	if op.Subcommand == "" {
		return op.Tool
	}
	return op.Tool + " " + op.Subcommand
}

// noHooksConfig points git at a hooks directory that cannot exist, which
// disables every hook, including the per-commit hook installed through the
// worktree's core.hooksPath (FDR 0019).
var noHooksConfig = []string{"-c", "core.hooksPath=/dev/null"}

// withoutHooksForConfined disables git hooks for a confined caller's mutating
// verb. The hooks live in the worker-writable worktree, so running them in the
// frontend would execute worker-authored code outside the unit (FDR 0036
// section 9.3); the merge gate is the check that remains. commit also gets an
// explicit --no-verify so the intent survives a hooks path override.
func withoutHooksForConfined(s store.Session, op store.Op, argv []string) []string {
	// fetch is a read verb, but it still updates refs and so fires
	// reference-transaction from the worker-writable hooks directory.
	mutates := store.ClassOf(op.Tool, op.Subcommand) == store.ClassWrite || (op.Tool == "remote" && op.Subcommand == "fetch")
	if !s.Confinement.Confined() || !mutates {
		return argv
	}
	if op.Tool == "commit" && len(argv) > 0 && argv[0] == "commit" {
		argv = append([]string{"commit", "--no-verify"}, argv[1:]...)
	}
	return append(append([]string{}, noHooksConfig...), argv...)
}

var commitCreatingVerbs = map[string]bool{
	"commit ":            true,
	"branch cherry-pick": true,
	"branch revert":      true,
	"branch rebase":      true,
	"branch restack":     true,
	"merge ":             true,
	"remote pull":        true,
	"tag create":         true,
}

// identityEnv stamps the configured identity onto the verbs that create
// commits. Unset fields add nothing, so git resolves the identity itself.
func identityEnv(s store.Session, op store.Op) []string {
	if !commitCreatingVerbs[op.Tool+" "+op.Subcommand] {
		return nil
	}
	return s.Identity.Env()
}
