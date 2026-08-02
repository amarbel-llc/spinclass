// Package basebranch decides which commit a new session's branch is cut from.
//
// `git worktree add -b <branch> <path>` with no start-point bases the new
// branch on whatever HEAD the checkout happens to hold, and nothing fetches
// first — so a session could silently inherit an arbitrarily old tree, or a
// tree from an unrelated feature branch the operator left checked out
// (spinclass#250). That is not merely stale source: flake.lock is part of the
// tree, so a stale base materializes a stale devShell, and every tool pinned
// below it — formatter, codegen, linters, the pre-commit wrapper — comes from
// the old lock. The observed failure was a session silently regenerating a
// generated file with a pre-rename module path on every commit.
//
// Freshen is the answer: resolve the default branch, fetch it, fast-forward the
// LOCAL default branch when — and only when — that is a pure fast-forward, and
// return the resulting sha for the caller to pass as an explicit start-point.
//
// All the policy lives here; internal/git holds only verbs. The package
// deliberately has no sweatfile dependency — the override arrives as a plain
// bool the caller has already resolved — which is what keeps every branch of
// this decision testable against a bare git fixture.
package basebranch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/git"
)

// ErrStaleBase reports that the default branch could not be brought up to date
// and the caller required that it be. Wrapped errors always name the override,
// because the operator's next move is either to fix the repo or to opt out and
// only they can judge which.
var ErrStaleBase = errors.New("stale base")

// fetchTimeout bounds the single network operation in this package. It exists
// for spawn-session specifically: that path waits on a hello with its own
// deadline and points stdio at a log file, so a fetch blocked on an
// unreachable host would surface as a spawn timeout with nothing to explain it.
const fetchTimeout = 30 * time.Second

// Action records what Freshen did to the local default branch.
type Action int

const (
	// Advanced: the local default branch was fast-forwarded to the fetched tip.
	Advanced Action = iota
	// AlreadyCurrent: it already matched the fetched tip.
	AlreadyCurrent
	// SkippedNoRemote: nothing to be stale against.
	SkippedNoRemote
	// SkippedAmbiguous: the default branch could not be named at all, so there
	// is no base to resolve and the caller falls back to HEAD.
	SkippedAmbiguous
	// SkippedAhead: the local default branch CONTAINS the fetched tip. Not
	// stale — routine after any --local-only merge — and never an error.
	SkippedAhead
	// SkippedStale: the default branch is genuinely out of date and could not
	// be advanced, but the caller tolerated it (resume, or an explicit
	// override).
	SkippedStale
)

// Skipped reports whether the local default branch was left untouched.
func (a Action) Skipped() bool { return a != Advanced && a != AlreadyCurrent }

// Result describes the outcome of a Freshen call.
type Result struct {
	// Branch is the resolved default branch, or "" when it could not be named.
	Branch string
	// BaseSha is the start-point for `git worktree add -b`. Empty means the
	// caller should omit the start-point and let git use HEAD — the pre-#250
	// behaviour, and the only safe answer when no default branch was resolved.
	//
	// A sha rather than the branch name, deliberately: it is immune to another
	// process moving the branch between here and the worktree add, and a sha
	// start-point cannot trip branch.autoSetupMerge into silently giving the
	// session branch an upstream.
	BaseSha string
	Action  Action
	// Reason is a human-readable detail for the caller's report line. Empty on
	// the two success actions.
	Reason string
}

// Freshen resolves repoPath's default branch, fetches it, and fast-forwards the
// local default branch to the fetched tip when that is a pure fast-forward. It
// never prompts, never rewrites history, and never moves any branch other than
// the default.
//
// required distinguishes the two callers. On session creation (required) a
// default branch that could not be verified current is an error: that is the
// whole point, since the new worktree is about to inherit it. On resume
// (!required) every problem degrades to a skip and Freshen never returns an
// error — refusing to reattach to an existing session because a remote is
// unreachable would be a regression, not a safeguard.
//
// allowStale suppresses the error in both directions. It is the deliberate
// offline / pinned-checkout escape hatch.
//
// Note that a skip still carries a BaseSha whenever the default branch was
// resolvable. Cutting from a possibly-stale default branch is strictly better
// than cutting from HEAD, which may be an unrelated feature branch — the
// freshness problem and the wrong-branch problem are independent, and only the
// first one is conditional.
func Freshen(ctx context.Context, repoPath string, allowStale, required bool) (Result, error) {
	branch := resolveDefault(repoPath)
	if branch == "" {
		return Result{
			Action: SkippedAmbiguous,
			Reason: "could not determine the default branch",
		}, nil
	}

	// Every skip below still bases on the local default branch.
	skip := func(action Action, reason string) Result {
		return Result{
			Branch:  branch,
			BaseSha: localSha(repoPath, branch),
			Action:  action,
			Reason:  reason,
		}
	}

	// stale is the one place the required/allowStale contract is applied, so
	// the fatal-vs-tolerated decision cannot drift between conditions.
	stale := func(reason string) (Result, error) {
		res := skip(SkippedStale, reason)
		if required && !allowStale {
			return res, fmt.Errorf(
				"%w: %s\n\npass --allow-stale-base, or set [hooks].allow-stale-base, to "+
					"create the session from the local %s anyway",
				ErrStaleBase, reason, branch,
			)
		}
		return res, nil
	}

	// A repo with no remote has nothing to be stale against. Checked before
	// anything reaches the network, which also keeps every remote-less fixture
	// repo on a purely local path.
	remotes := git.Remotes(repoPath)
	if len(remotes) == 0 {
		return skip(SkippedNoRemote, "no remote configured"), nil
	}
	remote := remoteFor(repoPath, branch, remotes)
	if remote == "" {
		return skip(SkippedNoRemote, "no remote tracks "+branch), nil
	}

	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	if out, err := git.FetchContext(fctx, repoPath, remote, branch); err != nil {
		return stale(fmt.Sprintf("could not fetch %s from %s: %v%s",
			branch, remote, err, indentDetail(out)))
	}

	localRef := "refs/heads/" + branch
	remoteRef := "refs/remotes/" + remote + "/" + branch

	local := localSha(repoPath, branch)
	upstream, err := git.RevParse(repoPath, remoteRef)
	if err != nil || local == "" {
		// The explicit fetch refspec should have written remoteRef, so this is
		// an unusual repo (a non-standard refspec, a shallow graft). Nothing is
		// verifiable, so treat it as unverified rather than guessing.
		return stale(fmt.Sprintf("could not compare %s against %s/%s", branch, remote, branch))
	}

	switch {
	case local == upstream:
		return Result{Branch: branch, BaseSha: local, Action: AlreadyCurrent}, nil

	case git.IsAncestor(repoPath, remoteRef, localRef):
		// Local contains upstream. Normal immediately after a --local-only
		// merge, so treating it as staleness would be a routine false positive.
		return skip(SkippedAhead, fmt.Sprintf("local %s is ahead of %s/%s", branch, remote, branch)), nil

	case !git.IsAncestor(repoPath, localRef, remoteRef):
		return stale(fmt.Sprintf(
			"local %s has diverged from %s/%s; rebase or reset it in the checkout",
			branch, remote, branch,
		))
	}

	behind := git.CommitsAhead(repoPath, localRef, remoteRef)
	holder, err := git.BranchWorktree(repoPath, branch)
	if err != nil {
		return stale(fmt.Sprintf("could not locate the worktree holding %s: %v", branch, err))
	}

	if holder == "" {
		// No worktree has it checked out, so the branch ref can be moved
		// directly — no working tree is involved and nothing can be in the way.
		if err := git.BranchSetTo(repoPath, branch, remoteRef); err != nil {
			return stale(fmt.Sprintf("could not advance %s to %s/%s: %v", branch, remote, branch, err))
		}
		return Result{
			Branch:  branch,
			BaseSha: localSha(repoPath, branch),
			Action:  Advanced,
			Reason:  fmt.Sprintf("fast-forwarded %s (%d commits)", branch, behind),
		}, nil
	}

	// Some worktree has it checked out, so the move has to go through a merge
	// there. Attempt it and let git decide rather than pre-screening for dirt:
	// uncommitted changes to files the fast-forward does not touch are fine,
	// and only git knows which those are. The failure is then explained after
	// the fact, which also covers a lost race against a concurrent merge.
	if out, err := git.MergeFFOnly(holder, remoteRef); err != nil {
		reason := fmt.Sprintf("local %s is %d commits behind %s/%s and could not be fast-forwarded",
			branch, behind, remote, branch)
		if git.HasDirtyTracked(holder) {
			reason += fmt.Sprintf(": %s has uncommitted changes; commit or stash them", holder)
		} else {
			reason += fmt.Sprintf(" in %s: %v%s", holder, err, indentDetail(out))
		}
		return stale(reason)
	}

	return Result{
		Branch:  branch,
		BaseSha: localSha(repoPath, branch),
		Action:  Advanced,
		Reason:  fmt.Sprintf("fast-forwarded %s (%d commits)", branch, behind),
	}, nil
}

// resolveDefault names repoPath's default branch without ever prompting, or
// returns "" when it cannot.
//
// Deliberately NOT merge.ResolveDefaultBranch: that huh-prompts when both main
// and master exist. This runs on the spawn-session path, where stdio is a log
// file and nobody is there to answer — a prompt would be an unrecoverable hang
// rather than a question. Returning "" instead degrades to the pre-#250
// HEAD-based behaviour, which is the same thing closeShop already does when it
// meets an ambiguous default branch non-interactively.
func resolveDefault(repoPath string) string {
	branch, err := git.DefaultBranch(repoPath)
	if err == nil {
		return branch
	}
	if !errors.Is(err, git.ErrAmbiguousDefaultBranch) {
		return ""
	}
	// Both main and master exist. The remote's published HEAD is the only
	// tie-breaker available without asking someone.
	const prefix = "refs/remotes/origin/"
	out, err := git.Run(repoPath, "symbolic-ref", prefix+"HEAD")
	if err != nil || !strings.HasPrefix(out, prefix) {
		return ""
	}
	return strings.TrimPrefix(out, prefix)
}

// remoteFor picks the remote to fetch branch from: the branch's configured
// tracking remote, else origin, else a sole remote. With several remotes and no
// tracking configuration there is no defensible choice, so it returns "" and
// the caller skips rather than guessing.
func remoteFor(repoPath, branch string, remotes []string) string {
	if r, err := git.Run(repoPath, "config", "--get", "branch."+branch+".remote"); err == nil && r != "" {
		return r
	}
	for _, r := range remotes {
		if r == "origin" {
			return "origin"
		}
	}
	if len(remotes) == 1 {
		return remotes[0]
	}
	return ""
}

// localSha resolves branch's local ref, or "" when it has none. The full
// refs/heads/ form avoids resolving a tag that shares the branch's name.
func localSha(repoPath, branch string) string {
	sha, err := git.RevParse(repoPath, "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return sha
}

// indentDetail renders captured git output as an indented block beneath a
// reason line, or "" when there was none.
func indentDetail(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return "\n" + strings.Join(lines, "\n")
}
