package session

import "path/filepath"

// ListRow is one machine-readable row of `sc list --format json`. The
// field names and their order are a wire contract: remote hosts serve
// this exact shape over `ssh <host> spinclass list --format json` (see
// docs/plans/2026-06-06-remote-sessions-design.md). Do not rename or
// reorder fields.
type ListRow struct {
	ID          string `json:"id"`          // worktree dir basename
	SessionKey  string `json:"session_key"` // <repo>/<branch>
	State       string `json:"state"`       // resolved: active|inactive|running-detached|abandoned
	Description string `json:"description"`
	Repo        string `json:"repo"` // repo dir basename
}

// ListRows converts states to wire rows, mirroring the text output's
// filter semantics: entries that resolve to abandoned (tombstones,
// dangling symlinks, missing worktrees) are skipped unless closed is
// true. The result is never nil so JSON marshaling yields [] rather
// than null.
func ListRows(states []State, closed bool) []ListRow {
	rows := []ListRow{}
	for _, s := range states {
		resolved := s.ResolveState()
		if resolved == StateAbandoned && !closed {
			continue
		}
		rows = append(rows, ListRow{
			ID:          filepath.Base(s.WorktreePath),
			SessionKey:  s.SessionKey,
			State:       resolved,
			Description: s.Description,
			Repo:        filepath.Base(s.RepoPath),
		})
	}
	return rows
}
