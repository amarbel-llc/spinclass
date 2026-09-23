// Package landing names where a merge lands and where a new session is cut
// from (spinclass#315).
//
// The principle: a merge targets the default REMOTE's default branch. The
// root checkout's LOCAL default branch is advanced for ergonomics only — it is
// never read for correctness on a remote target, and failing to advance it is
// never a failure, only a reported skip. A local-only merge (gitSync=false),
// and a repo with no remote, is modeled as a merge whose remote is SELF: its
// target is the local default branch, fetching is a no-op, and landing IS the
// fast-forward of the local branch (so there, and only there, the fast-forward
// is fatal).
//
// Both targets share one primitive — fast-forward refs/heads/<default> through
// the worktree that has it checked out, or move the ref when none does — which
// is why "land on self" and "advance local after a remote landing" differ only
// in whether a refusal is an error.
package landing

import (
	"context"
	"fmt"
	"strings"

	"code.linenisgreat.com/spinclass/internal/git"
)

// ManPage is cited by every local-advance skip so the reader learns why the
// skip is acceptable and how to reconcile.
const ManPage = "spinclass-local-default-ref(7)"

// Target is a merge's landing destination and a new session's base.
type Target struct {
	RepoPath string
	// Branch is the default branch name (e.g. "master").
	Branch string
	// Remote is the remote whose Branch is the target; "" means self.
	Remote string
}

// Remote is the target for a merge that lands on remote's default branch.
func Remote(repoPath, branch, remote string) Target {
	return Target{RepoPath: repoPath, Branch: branch, Remote: remote}
}

// Self is the target for a local-only merge: the local default branch.
func Self(repoPath, branch string) Target {
	return Target{RepoPath: repoPath, Branch: branch}
}

// ForMerge picks the target for a merge: the default branch's configured
// remote (git.BranchRemote: branch.<name>.remote, else origin) when gitSync,
// else self.
func ForMerge(repoPath, branch string, gitSync bool) Target {
	if !gitSync {
		return Self(repoPath, branch)
	}
	return Remote(repoPath, branch, git.BranchRemote(repoPath, branch))
}

func (t Target) IsSelf() bool { return t.Remote == "" }

// LocalRef is the local default branch's full ref name.
func (t Target) LocalRef() string { return "refs/heads/" + t.Branch }

// Ref is the full ref the target resolves to: the remote-tracking ref for a
// remote target, the local branch for self. Everything that decides what a
// merge rebases onto, or what a session is cut from, reads this.
func (t Target) Ref() string {
	if t.IsSelf() {
		return t.LocalRef()
	}
	return "refs/remotes/" + t.Remote + "/" + t.Branch
}

// Label is Ref's short human form ("origin/master" or "master").
func (t Target) Label() string {
	if t.IsSelf() {
		return t.Branch
	}
	return t.Remote + "/" + t.Branch
}

// Fetch refreshes Ref from the remote, running from credDir (the worktree
// carrying any per-session credential wiring, FDR 0028). A no-op for self.
// A fetch failure is the one fatal freshness failure: without it Ref may be
// arbitrarily stale.
func (t Target) Fetch(ctx context.Context, credDir string) (string, error) {
	if t.IsSelf() {
		return "", nil
	}
	return git.FetchContext(ctx, credDir, t.Remote, t.Branch)
}

// Land publishes sha as the new target tip. Remote: push sha from landDir —
// the remote applies its own fast-forward check, so a refused push moves
// nothing, locally or remotely. Self: fast-forward the local branch; a
// refusal is an error, since there the fast-forward IS the landing.
func (t Target) Land(landDir, sha string) (string, error) {
	if !t.IsSelf() {
		return git.PushRef(landDir, t.Remote, sha, t.Branch)
	}
	adv := fastForward(t.RepoPath, t.Branch, sha, shortSha(sha))
	if adv.Skipped() {
		return adv.Detail, fmt.Errorf("%s", adv.Reason)
	}
	return "", nil
}

// AdvanceLocal opportunistically fast-forwards the local default branch to
// rev (a sha or ref the target now holds). Never an error: a refusal comes
// back as a skipped Advance for the caller to report. A no-op for self, whose
// landing already moved the local branch.
func (t Target) AdvanceLocal(rev string) Advance {
	if t.IsSelf() {
		return Advance{Outcome: Current, Branch: t.Branch, To: t.Branch}
	}
	return fastForward(t.RepoPath, t.Branch, rev, t.Label())
}

// Outcome classifies an attempt to fast-forward the local default branch.
type Outcome int

const (
	// Advanced: the local branch moved forward.
	Advanced Outcome = iota
	// Current: it already pointed at the target.
	Current
	// Ahead: it already contains the target plus commits the target lacks.
	Ahead
	// Diverged: it and the target each have commits the other lacks.
	Diverged
	// Blocked: git refused the fast-forward in the worktree holding the
	// branch — typically uncommitted changes overlapping the incoming ones.
	Blocked
	// Failed: the attempt could not be made (missing branch, git error).
	Failed
)

// Advance reports what happened to the local default branch.
type Advance struct {
	Outcome Outcome
	Branch  string
	// To is the human label of what the branch was advanced toward.
	To string
	// Holder is the worktree that has Branch checked out, "" when none does.
	Holder string
	// Reason explains a skip; empty otherwise.
	Reason string
	// Reconcile is the command (or instruction) that brings the local branch
	// up to date by hand; empty unless skipped.
	Reconcile string
	// Detail is captured git output behind a Blocked or Failed skip.
	Detail string
	// Dirty marks a Blocked skip caused by uncommitted changes in Holder.
	Dirty bool
}

// Skipped reports whether the local branch was left behind the target.
func (a Advance) Skipped() bool { return a.Outcome >= Ahead }

// SkipSlug names a skip's cause as one metric-name segment (spinclass#314):
// dirty_overlap, diverged, ahead, or error. "" when not skipped.
func (a Advance) SkipSlug() string {
	switch {
	case !a.Skipped():
		return ""
	case a.Outcome == Ahead:
		return "ahead"
	case a.Outcome == Diverged:
		return "diverged"
	case a.Outcome == Blocked && a.Dirty:
		return "dirty_overlap"
	default:
		return "error"
	}
}

// SkipReason renders a skip as the one-line reason for a TAP/crap `# SKIP`
// directive: why, how to reconcile, and where the reasoning is documented.
func (a Advance) SkipReason() string {
	msg := a.Reason
	if a.Reconcile != "" {
		msg += "; reconcile: " + a.Reconcile
	}
	return msg + " (see " + ManPage + ")"
}

// fastForward moves refs/heads/<branch> in repoPath forward to rev, labelled
// toLabel in reasons. It never rewinds and never creates the branch.
func fastForward(repoPath, branch, rev, toLabel string) Advance {
	adv := Advance{Branch: branch, To: toLabel}
	localRef := "refs/heads/" + branch

	local, err := git.RevParse(repoPath, localRef)
	if err != nil {
		adv.Outcome = Failed
		adv.Reason = fmt.Sprintf("local %s does not exist", branch)
		return adv
	}
	target, err := git.RevParse(repoPath, rev+"^{commit}")
	if err != nil {
		adv.Outcome = Failed
		adv.Reason = fmt.Sprintf("could not resolve %s", toLabel)
		return adv
	}
	if local == target {
		adv.Outcome = Current
		return adv
	}

	holder, err := git.BranchWorktree(repoPath, branch)
	if err != nil {
		adv.Outcome = Failed
		adv.Reason = fmt.Sprintf("could not locate the worktree holding %s: %v", branch, err)
		return adv
	}
	adv.Holder = holder
	dir := holder
	if dir == "" {
		dir = repoPath
	}

	switch {
	case git.IsAncestor(repoPath, target, local):
		adv.Outcome = Ahead
		adv.Reason = fmt.Sprintf("local %s is ahead of %s", branch, toLabel)
		adv.Reconcile = fmt.Sprintf("push or drop the local-only commits on %s (git -C %s log %s..%s)", branch, dir, target, localRef)
		return adv
	case !git.IsAncestor(repoPath, local, target):
		adv.Outcome = Diverged
		adv.Reason = fmt.Sprintf("local %s has diverged from %s", branch, toLabel)
		adv.Reconcile = fmt.Sprintf("rebase or reset local %s onto %s in %s", branch, toLabel, dir)
		return adv
	}

	if holder == "" {
		if err := git.BranchSetTo(repoPath, branch, target); err != nil {
			adv.Outcome = Failed
			adv.Reason = fmt.Sprintf("could not move %s to %s: %v", branch, toLabel, err)
			adv.Reconcile = fmt.Sprintf("git -C %s branch -f %s %s", repoPath, branch, target)
			return adv
		}
		adv.Outcome = Advanced
		return adv
	}

	// Let git decide rather than pre-screening for dirt: uncommitted changes
	// to files the fast-forward does not touch are fine, and only git knows
	// which those are.
	if out, err := git.MergeFFOnly(holder, target); err != nil {
		adv.Outcome = Blocked
		adv.Detail = strings.TrimSpace(out)
		adv.Reconcile = fmt.Sprintf("git -C %s merge --ff-only %s", holder, target)
		if git.HasDirtyTracked(holder) {
			adv.Dirty = true
			adv.Reason = fmt.Sprintf("uncommitted changes in %s block the fast-forward of %s", holder, branch)
			adv.Reconcile = "commit or stash them, then " + adv.Reconcile
		} else {
			adv.Reason = fmt.Sprintf("git refused to fast-forward %s in %s: %v", branch, holder, err)
		}
		return adv
	}
	adv.Outcome = Advanced
	return adv
}

func shortSha(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
