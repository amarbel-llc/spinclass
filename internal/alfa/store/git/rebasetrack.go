package git

import (
	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/alfa/store"
)

// rebaseStart is the full ref of the branch a rebase or restack that is about
// to start will replay: the rebase_branch argument (HEAD when empty) resolved
// the way git would, or for a restack the branch HEAD is on. It is read BEFORE
// git runs, from the arguments grit already judged, never from the state git
// leaves in a directory a unit can write. ok is false for a verb that does not
// start a rebase (continue, skip and abort resume one) or a target that does
// not resolve to a branch.
func rebaseStart(s store.Session, op store.Op, dir string) (ref string, ok bool) {
	if op.Tool != "branch" || (op.Subcommand != "rebase" && op.Subcommand != "restack") {
		return "", false
	}
	a, decoded := decodeClassifyArgs(op.Args)
	if !decoded {
		return "", false
	}
	c := checker{session: s, args: a, dir: dir}
	if op.Subcommand == "restack" {
		head, err := git.Run(dir, "symbolic-ref", "-q", "HEAD")
		return head, err == nil && isBranchRef(head)
	}
	for _, key := range []string{"abort", "continue", "skip"} {
		if a.flag(key) {
			return "", false
		}
	}
	target := "HEAD"
	if name, present := a.str("rebase_branch"); present && name != "" {
		target = name
	}
	full, resolved := c.canonicalRef(target)
	return full, resolved && isBranchRef(full)
}

func isBranchRef(ref string) bool {
	return len(ref) > len("refs/heads/") && ref[:len("refs/heads/")] == "refs/heads/"
}

// trackRebase keeps the frontend's rebase record in step with what a rebase
// verb did (spinclass#456). After a start that left a rebase in progress (a
// conflict stop) it records the branch captured before git ran; after any
// rebase verb that left none (finished, aborted, or never started) it clears
// the record. A session with no record store keeps nothing.
func trackRebase(s store.Session, op store.Op, dir, started string) {
	if s.Rebases == nil || op.Tool != "branch" || (op.Subcommand != "rebase" && op.Subcommand != "restack") {
		return
	}
	root := s.Dir
	if root == "" {
		root = dir
	}
	inProgress := gitPathExists(dir, "rebase-merge") || gitPathExists(dir, "rebase-apply")
	if !inProgress {
		s.Rebases.Clear(root)
		return
	}
	if started != "" {
		_ = s.Rebases.Write(root, started)
	}
}
