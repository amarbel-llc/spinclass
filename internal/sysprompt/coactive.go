package sysprompt

import (
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/session"
)

// loadCoActiveLine is the production co-active-sessions loader (spinclass#238):
// it returns a one-line summary of the OTHER active sessions on the same repo
// ("2 other live sessions on <repo>: …"), or "" when there are none. path is
// the session's own worktree (worktree mode) or the checkout root (main-
// checkout mode); it is both the repo-resolution anchor and the self-exclusion
// key.
//
// Local file I/O only — session state JSON under the central index plus PID
// liveness via signal 0 — so it is safe before `initialize`. Best-effort is a
// hard guarantee (mirroring renderDesignRecords): any error, and any
// unexpected panic via the recover, yields "" — the line is simply omitted,
// never a failed render.
func loadCoActiveLine(mode Mode, path string) (line string) {
	defer func() {
		if recover() != nil {
			line = ""
		}
	}()
	if path == "" {
		return ""
	}
	// The repo anchor: a worktree's own session state carries RepoPath; a
	// main checkout IS the repo path.
	repoPath := path
	if mode == ModeWorktree {
		st, err := session.FindByWorktreePath(path)
		if err != nil || st.RepoPath == "" {
			return ""
		}
		repoPath = st.RepoPath
	}
	others, err := session.ListActiveForRepoExcluding(repoPath, path)
	if err != nil || len(others) == 0 {
		return ""
	}
	return formatCoActiveLine(filepath.Base(repoPath), others)
}

// formatCoActiveLine renders "N other live session(s) on <repo>: a (desc), b".
func formatCoActiveLine(repo string, others []session.State) string {
	entries := make([]string, len(others))
	for i := range others {
		entry := others[i].BranchOrKey()
		if d := others[i].Description; d != "" {
			entry += " (" + d + ")"
		}
		entries[i] = entry
	}
	return session.CoActiveSummary("other live", repo, entries)
}
