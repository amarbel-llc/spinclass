// Package resurrect recreates a closed session's worktree and branch from
// the commit captured by internal/close.RunResolved immediately before
// `sc close`/`close-child-session` force-delete them (#291). It is the undo
// half of the close lifecycle: session.Tombstone records DeletedSHA at close
// time, and Run here rebuilds a live worktree from it.
package resurrect

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/worktree"

	tap "code.linenisgreat.com/tap/go/pkgs/writer"
)

// Run resolves target (a worktree directory name or <repo>/<branch> session
// key, the same grammar session.FindByTarget accepts for close/resume)
// against a CLOSED session, recreates its worktree and branch from the
// commit captured at close time, and writes fresh (inactive) session state —
// so `sc list`/`sc resume` see it as a normal session again. Does not
// attach; callers run `sc resume` separately, reusing that already-hardened
// path unmodified.
//
// newBranchName, when non-empty, names the recreated branch instead of
// reusing the original — useful when something else already created a
// branch with that name since the original was deleted.
func Run(w io.Writer, target, newBranchName, format string) error {
	st, err := session.FindByTarget(target)
	if err != nil {
		return err
	}

	if err := checkResurrectable(st); err != nil {
		return err
	}

	branch := st.Branch
	if newBranchName != "" {
		if err := worktree.ValidateName(newBranchName); err != nil {
			return err
		}
		branch = newBranchName
	}
	newPath := filepath.Join(st.RepoPath, worktree.WorktreesDir, branch)

	if _, err := worktree.Create(st.RepoPath, newPath, "", st.DeletedSHA); err != nil {
		return fmt.Errorf("recreating worktree for %s: %w", st.Key(), err)
	}

	fresh := session.State{
		SessionState: session.StateInactive,
		RepoPath:     st.RepoPath,
		WorktreePath: newPath,
		Branch:       branch,
		SessionKey:   filepath.Base(st.RepoPath) + "/" + branch,
		Description:  st.Description,
		SpawnedBy:    st.SpawnedBy,
		StartedAt:    time.Now(),
	}
	if err := session.Write(fresh); err != nil {
		return fmt.Errorf("writing resurrected session state for %s: %w", fresh.SessionKey, err)
	}

	if format == "tap" {
		tw := tap.NewWriter(w)
		tw.PlanAhead(1)
		tw.Ok("resurrect " + fresh.SessionKey + " " + newPath)
		return nil
	}
	_, _ = fmt.Fprintln(w, newPath)
	return nil
}

// checkResurrectable holds the three refusal conditions Run's callers need
// distinct, actionable errors for: not actually closed, closed with no
// captured commit (predates this feature, or closed outside spinclass), and
// a captured commit that's no longer reachable (most likely local git gc).
func checkResurrectable(st *session.State) error {
	if !st.IsTombstone() {
		return fmt.Errorf("%s is not a closed session — nothing to resurrect", st.Key())
	}
	if st.DeletedSHA == "" {
		return fmt.Errorf(
			"%s has no captured commit (closed before `sc resurrect` support, or closed "+
				"outside spinclass); recover manually via `git reflog` in %s",
			st.Key(), st.RepoPath,
		)
	}
	if !git.CommitExists(st.RepoPath, st.DeletedSHA) {
		sha := st.DeletedSHA
		if len(sha) > 12 {
			sha = sha[:12]
		}
		return fmt.Errorf(
			"commit %s for %s is no longer reachable (likely garbage collected)",
			sha, st.Key(),
		)
	}
	return nil
}
