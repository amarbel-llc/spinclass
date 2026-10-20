package git

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/store"
)

// ownershipSeparator joins a session branch to a topic suffix; generated
// session names are single-hyphen words, so no session namespace is a prefix
// of another's (FDR 0036 section 3).
const ownershipSeparator = "--"

// Owned reports whether ref belongs to the session. ref must be canonical: a
// full ref (refs/heads/, refs/tags/, refs/remotes/<remote>/, refs/spinclass/)
// or an absolute worktree path. Anything else, including a short name or a
// ref using revision syntax, is never owned.
func (Backend) Owned(s store.Session, ref store.Ref) bool {
	name := string(ref)
	if filepath.IsAbs(name) {
		return s.Dir != "" && sameDir(name, s.Dir)
	}
	if s.Branch == "" || !plainRefName(name) {
		return false
	}
	switch {
	case strings.HasPrefix(name, "refs/heads/"):
		return ownsBranchName(s, strings.TrimPrefix(name, "refs/heads/"))
	case strings.HasPrefix(name, "refs/remotes/"):
		_, short, found := strings.Cut(strings.TrimPrefix(name, "refs/remotes/"), "/")
		return found && ownsBranchName(s, short)
	case strings.HasPrefix(name, "refs/tags/"):
		return strings.HasPrefix(strings.TrimPrefix(name, "refs/tags/"), s.Branch+ownershipSeparator)
	case strings.HasPrefix(name, "refs/spinclass/"+s.Branch+"/"):
		return true
	}
	return false
}

func ownsBranchName(s store.Session, short string) bool {
	if isDefaultBranch(s.Dir, short) {
		return false
	}
	return short == s.Branch || strings.HasPrefix(short, s.Branch+ownershipSeparator)
}

// isDefaultBranch is true for main, master and the origin/HEAD target: the
// default branch is never owned, whatever the session branch is called.
func isDefaultBranch(dir, short string) bool {
	if short == "main" || short == "master" {
		return true
	}
	if dir == "" {
		return false
	}
	out, err := git.Run(dir, "symbolic-ref", "-q", "refs/remotes/origin/HEAD")
	return err == nil && strings.TrimPrefix(out, "refs/remotes/origin/") == short
}

// plainRefName rejects every form that is not one literal ref name: revision
// syntax (.., ^, ~, @{), option-likes, and characters git forbids in refs.
func plainRefName(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") ||
		strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{") {
		return false
	}
	for _, r := range name {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	return true
}

func sameDir(a, b string) bool {
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && resolvedA == resolvedB
}

// Classify is the permission reading of an invocation (FDR 0036 section 2):
// read verbs allow, write verbs allow iff every target is session-owned,
// floor verbs and anything force-flagged ask. It fails closed on any argument
// it cannot interpret and performs no side effect.
func (b Backend) Classify(s store.Session, op store.Op) store.Verdict {
	a, ok := decodeClassifyArgs(op.Args)
	if !ok || a.flag("force") || !a.optionFree() {
		return store.Ask
	}
	switch store.ClassOf(op.Tool, op.Subcommand) {
	case store.ClassRead:
		return store.Allow
	case store.ClassWrite:
		if b.writeAllowed(s, op, a) {
			return store.Allow
		}
	}
	return store.Ask
}

type classifyArgs map[string]any

func decodeClassifyArgs(raw json.RawMessage) (classifyArgs, bool) {
	a := classifyArgs{}
	if len(raw) == 0 {
		return a, true
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, false
	}
	return a, true
}

// str reads a string argument; absent is "" and a wrong type is not ok.
func (a classifyArgs) str(key string) (string, bool) {
	v, present := a[key]
	if !present || v == nil {
		return "", true
	}
	s, ok := v.(string)
	return s, ok
}

func (a classifyArgs) strs(key string) ([]string, bool) {
	v, present := a[key]
	if !present || v == nil {
		return nil, true
	}
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(items))
	for i, item := range items {
		if out[i], ok = item.(string); !ok {
			return nil, false
		}
	}
	return out, true
}

// flag fails closed: anything other than absent, null or false is set.
func (a classifyArgs) flag(key string) bool {
	v, present := a[key]
	if !present || v == nil {
		return false
	}
	b, ok := v.(bool)
	return !ok || b
}

var optionScannedScalars = []string{
	"ref", "name", "contains", "branch", "remote_branch", "root", "onto", "upstream",
	"rebase_branch", "start_point", "pattern", "remote", "destination", "source", "stash_ref",
}

// optionFree is false when any ref-like value could be read by git as an
// option, or has a type that cannot be interpreted.
func (a classifyArgs) optionFree() bool {
	for _, key := range optionScannedScalars {
		v, ok := a.str(key)
		if !ok || strings.HasPrefix(v, "-") {
			return false
		}
	}
	for _, key := range []string{"commits", "branches"} {
		vs, ok := a.strs(key)
		if !ok {
			return false
		}
		for _, v := range vs {
			if strings.HasPrefix(v, "-") {
				return false
			}
		}
	}
	return true
}

// checker evaluates one write invocation against the session.
type checker struct {
	session store.Session
	args    classifyArgs
	dir     string
}

func (b Backend) writeAllowed(s store.Session, op store.Op, a classifyArgs) bool {
	c := checker{session: s, args: a}
	if !c.bindsToSessionWorktree() {
		return false
	}
	switch op.Tool + " " + op.Subcommand {
	case "stash save", "stash apply", "stash drop":
		return true
	case "stage add", "stage unstage", "stage rm", "stage mv", "commit ",
		"merge ", "remote pull", "branch reset-soft", "branch reset-hard",
		"branch cherry-pick", "branch revert":
		return c.currentBranchOwned()
	case "branch create":
		return c.newBranchOwned("name")
	case "branch delete":
		return c.existingBranchOwned("name")
	case "branch checkout":
		return c.checkoutOwned()
	case "branch rebase":
		return c.rebaseOwned()
	case "branch restack":
		return c.restackOwned()
	case "tag create", "tag delete":
		return c.tagOwned()
	case "tag push":
		return c.tagOwned() && !a.flag("delete")
	case "remote push":
		return c.pushOwned()
	case "remote push-stack":
		return c.pushStackOwned()
	}
	return false
}

// bindsToSessionWorktree requires the call to execute in the session's own
// worktree: no-target operations bind to its checked-out branch, so any other
// directory would let them act on someone else's.
func (c *checker) bindsToSessionWorktree() bool {
	if c.session.Dir == "" || c.session.Branch == "" {
		return false
	}
	repoPath, ok := c.args.str("repo_path")
	if !ok {
		return false
	}
	c.dir = repoPath
	if c.dir == "" {
		c.dir = c.session.Dir
	} else if !filepath.IsAbs(c.dir) {
		c.dir = filepath.Join(c.session.Dir, c.dir)
	}
	top, err := git.Run(c.dir, "rev-parse", "--show-toplevel")
	return err == nil && sameDir(top, c.session.Dir)
}

func (c checker) owned(fullRef string) bool {
	return Backend{}.Owned(c.session, store.Ref(fullRef))
}

func (c checker) currentBranchOwned() bool {
	head, err := git.Run(c.dir, "symbolic-ref", "-q", "HEAD")
	return err == nil && strings.HasPrefix(head, "refs/heads/") && c.owned(head)
}

// canonicalRef resolves a target to its full ref the way git itself would, so
// HEAD, @ and abbreviations cannot launder a foreign branch past a string
// prefix test. Revision syntax, detached HEAD and any failure return false.
func (c checker) canonicalRef(name string) (string, bool) {
	if name != "HEAD" && name != "@" && !plainRefName(name) {
		return "", false
	}
	out, err := git.Run(c.dir, "rev-parse", "--symbolic-full-name", name)
	if err != nil || strings.Contains(out, "\n") || !strings.HasPrefix(out, "refs/") || !plainRefName(out) {
		return "", false
	}
	return out, true
}

func (c checker) existingBranchOwned(key string) bool {
	name, ok := c.args.str(key)
	if !ok || name == "" {
		return false
	}
	full, ok := c.canonicalRef(name)
	return ok && strings.HasPrefix(full, "refs/heads/") && c.owned(full)
}

// newBranchOwned judges a branch that may not exist yet by its literal name.
func (c checker) newBranchOwned(key string) bool {
	name, ok := c.args.str(key)
	return ok && plainRefName(name) && c.owned("refs/heads/"+name)
}

func (c checker) checkoutOwned() bool {
	paths, ok := c.args.strs("paths")
	if !ok {
		return false
	}
	if len(paths) > 0 {
		return c.currentBranchOwned()
	}
	if c.args.flag("new_branch") {
		return c.newBranchOwned("ref")
	}
	return c.existingBranchOwned("ref")
}

func (c checker) rebaseOwned() bool {
	for _, key := range []string{"abort", "continue", "skip"} {
		if c.args.flag(key) {
			return c.currentBranchOwned()
		}
	}
	target := "HEAD"
	if name, ok := c.args.str("rebase_branch"); !ok {
		return false
	} else if name != "" {
		target = name
	}
	full, ok := c.canonicalRef(target)
	if !ok || !strings.HasPrefix(full, "refs/heads/") || !c.owned(full) {
		return false
	}
	if !c.args.flag("update_refs") {
		return true
	}
	upstream, ok := c.args.str("upstream")
	if !ok || !plainRefName(upstream) {
		return false
	}
	return !c.foreignBranchInRange(upstream + ".." + full)
}

func (c checker) restackOwned() bool {
	if !c.currentBranchOwned() {
		return false
	}
	root, ok := c.args.str("root")
	if !ok || root == "" {
		return false
	}
	full, ok := c.canonicalRef(root)
	if !ok || !strings.HasPrefix(full, "refs/heads/") || !c.owned(full) {
		return false
	}
	return !c.foreignBranchInRange(full + "^..HEAD")
}

// foreignBranchInRange is true when a rewrite of the commit range would also
// move a branch the session does not own (rebase --update-refs repositions
// every branch pointing into the range). It fails closed on any git error.
func (c checker) foreignBranchInRange(rangeSpec string) bool {
	commits, err := git.Run(c.dir, "rev-list", rangeSpec)
	if err != nil {
		return true
	}
	inRange := map[string]bool{}
	for _, sha := range strings.Fields(commits) {
		inRange[sha] = true
	}
	refs, err := git.Run(c.dir, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads")
	if err != nil {
		return true
	}
	for _, line := range strings.Split(refs, "\n") {
		sha, ref, found := strings.Cut(line, " ")
		if found && inRange[sha] && !c.owned(ref) {
			return true
		}
	}
	return false
}

func (c checker) tagOwned() bool {
	name, ok := c.args.str("name")
	return ok && plainRefName(name) && c.owned("refs/tags/"+name)
}

func (c checker) pushOwned() bool {
	if c.args.flag("delete") {
		return false
	}
	source := "HEAD"
	if name, ok := c.args.str("branch"); !ok {
		return false
	} else if name != "" {
		source = name
	}
	full, ok := c.canonicalRef(source)
	if !ok || !strings.HasPrefix(full, "refs/heads/") || !c.owned(full) {
		return false
	}
	destination, ok := c.args.str("remote_branch")
	if !ok {
		return false
	}
	if destination == "" {
		return true
	}
	destination = strings.TrimPrefix(destination, "refs/heads/")
	return plainRefName(destination) && c.owned("refs/heads/"+destination)
}

func (c checker) pushStackOwned() bool {
	branches, ok := c.args.strs("branches")
	if !ok || len(branches) == 0 {
		return false
	}
	for _, name := range branches {
		full, ok := c.canonicalRef(name)
		if !ok || !strings.HasPrefix(full, "refs/heads/") || !c.owned(full) {
			return false
		}
	}
	return true
}
