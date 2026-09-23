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
// Freshen is the answer: resolve the default branch, fetch it, and return the
// fetched remote tip as the start-point — the same landing target a merge
// from the session will land on (internal/landing, #315). The local default
// branch is then fast-forwarded opportunistically, for ergonomics only: a
// local branch that is ahead, diverged, or blocked by a dirty checkout is a
// reported skip, never a reason to refuse the session. Only a base that could
// not be verified at all (the fetch failed) is.
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
	"code.linenisgreat.com/spinclass/internal/landing"
)

// ErrStaleBase reports that the default branch could not be verified current
// and the caller required that it be. Wrapped errors always name the override,
// because the operator's next move is either to fix the repo or to opt out and
// only they can judge which.
var ErrStaleBase = errors.New("stale base")

// fetchTimeout bounds the single network operation in this package. It exists
// for spawn-session specifically: that path waits on a hello with its own
// deadline and points stdio at a log file, so a fetch blocked on an
// unreachable host would surface as a spawn timeout with nothing to explain it.
const fetchTimeout = 30 * time.Second

// Action records how the base was resolved.
type Action int

const (
	// Advanced: the base is the fetched tip, and the local default branch was
	// fast-forwarded to it.
	Advanced Action = iota
	// AlreadyCurrent: the base is the fetched tip, which the local default
	// branch already matched.
	AlreadyCurrent
	// LocalSkipped: the base is the fetched tip, but the local default branch
	// was left behind it (ahead, diverged, or blocked — Result.Local says
	// which). Ergonomic only; never an error.
	LocalSkipped
	// SkippedNoRemote: nothing to be stale against; the base is the local
	// default branch (the self landing target).
	SkippedNoRemote
	// SkippedAmbiguous: the default branch could not be named at all, so there
	// is no base to resolve and the caller falls back to HEAD.
	SkippedAmbiguous
	// SkippedStale: the base could not be verified current (the fetch failed),
	// but the caller tolerated it (resume, or an explicit override).
	SkippedStale
)

// Skipped reports whether the base itself was not freshened from a remote.
// LocalSkipped is NOT a base skip: its base is fresh.
func (a Action) Skipped() bool {
	return a == SkippedNoRemote || a == SkippedAmbiguous || a == SkippedStale
}

// Result describes the outcome of a Freshen call.
type Result struct {
	// Branch is the resolved default branch, or "" when it could not be named.
	Branch string
	// Target is the base's human label ("origin/main", or "main" for a
	// remote-less repo); "" when no base was resolved.
	Target string
	// BaseSha is the start-point for `git worktree add -b`. Empty means the
	// caller should omit the start-point and let git use HEAD — the pre-#250
	// behaviour, and the only safe answer when no default branch was resolved.
	//
	// A sha rather than a ref name, deliberately: it is immune to another
	// process moving the ref between here and the worktree add, and a sha
	// start-point cannot trip branch.autoSetupMerge into silently giving the
	// session branch an upstream.
	BaseSha string
	Action  Action
	// Reason is a human-readable detail for the caller's report line.
	Reason string
	// Local is the opportunistic local-branch advance, set whenever a fetch
	// succeeded (Advanced, AlreadyCurrent, LocalSkipped).
	Local landing.Advance
}

// Freshen resolves repoPath's default branch, fetches it, and returns the
// fetched tip as the base, fast-forwarding the local default branch to it when
// that is a pure fast-forward. It never prompts, never rewrites history, and
// never moves any branch other than the default.
//
// required distinguishes the two callers. On session creation (required) a
// base that could not be verified current — the fetch failed — is an error:
// the new worktree is about to inherit it. On resume (!required) every problem
// degrades to a skip and Freshen never returns an error — refusing to reattach
// to an existing session because a remote is unreachable would be a
// regression, not a safeguard.
//
// allowStale suppresses the error in both directions. It is the deliberate
// offline / pinned-checkout escape hatch.
func Freshen(ctx context.Context, repoPath string, allowStale, required bool) (Result, error) {
	branch := resolveDefault(repoPath)
	if branch == "" {
		return Result{
			Action: SkippedAmbiguous,
			Reason: "could not determine the default branch",
		}, nil
	}

	// A repo with no remote is the self landing target: nothing to be stale
	// against, and the base is the local default branch. Checked before
	// anything reaches the network, which also keeps every remote-less fixture
	// repo on a purely local path.
	noRemote := func(reason string) Result {
		return Result{
			Branch:  branch,
			Target:  branch,
			BaseSha: localSha(repoPath, branch),
			Action:  SkippedNoRemote,
			Reason:  reason,
		}
	}
	remotes := git.Remotes(repoPath)
	if len(remotes) == 0 {
		return noRemote("no remote configured"), nil
	}
	remote := remoteFor(repoPath, branch, remotes)
	if remote == "" {
		return noRemote("no remote tracks " + branch), nil
	}
	target := landing.Remote(repoPath, branch, remote)

	// stale is the one place the required/allowStale contract is applied, so
	// the fatal-vs-tolerated decision cannot drift between conditions. The
	// base falls back to the last-fetched tip, else the local branch.
	stale := func(reason string) (Result, error) {
		base, err := git.RevParse(repoPath, target.Ref())
		if err != nil {
			base = localSha(repoPath, branch)
		}
		res := Result{Branch: branch, Target: target.Label(), BaseSha: base, Action: SkippedStale, Reason: reason}
		if required && !allowStale {
			return res, fmt.Errorf(
				"%w: %s\n\npass --allow-stale-base, or set [hooks].allow-stale-base, to "+
					"create the session from the last-fetched %s anyway",
				ErrStaleBase, reason, target.Label(),
			)
		}
		return res, nil
	}

	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	if out, err := target.Fetch(fctx, repoPath); err != nil {
		return stale(fmt.Sprintf("could not fetch %s from %s: %v%s",
			branch, remote, err, indentDetail(out)))
	}

	upstream, err := git.RevParse(repoPath, target.Ref())
	if err != nil {
		// The explicit fetch refspec should have written the tracking ref, so
		// this is an unusual repo (a non-standard refspec, a shallow graft).
		return stale("could not resolve " + target.Label() + " after fetching it")
	}

	res := Result{Branch: branch, Target: target.Label(), BaseSha: upstream}
	res.Local = target.AdvanceLocal(target.Ref())
	switch res.Local.Outcome {
	case landing.Advanced:
		res.Action = Advanced
		res.Reason = "fast-forwarded local " + branch
	case landing.Current:
		res.Action = AlreadyCurrent
	default:
		res.Action = LocalSkipped
		res.Reason = res.Local.SkipReason()
	}
	return res, nil
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
