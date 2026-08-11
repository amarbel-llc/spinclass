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

	// Remote is the sweatfile [[remotes]] name a row was fetched from.
	// Set only on rows `sc list` merged in from a remote host; local
	// rows (and the wire payload a host serves about itself) omit it.
	Remote string `json:"remote,omitempty"`

	// Kind is the session kind: "implicit" for a main-checkout session,
	// empty for a normal worktree session. omitempty keeps the wire shape
	// unchanged for worktree sessions.
	Kind string `json:"kind,omitempty"`

	// Branch is a display-only hint of the checkout's current branch. For
	// implicit (main-checkout) sessions the branch is NOT part of the session
	// key (which is <repo>/<rand>), so it is surfaced separately here for
	// `sc list` / chat listing. Appended last and omitempty to keep the remote
	// wire shape backward-compatible.
	Branch string `json:"branch,omitempty"`

	// SpawnedBy is the driver session's key recorded when this session was
	// launched as a worker by `sc spawn`. Display-only
	// lineage (FDR 0006). Appended last and omitempty to keep the remote
	// wire shape backward-compatible.
	SpawnedBy string `json:"spawned_by,omitempty"`

	// ClownCount is the number of live clown harnesses running under this
	// session (clown presence records whose decoration == SessionKey, within
	// the staleness window). The 1-to-many "clowns under this session" view
	// (#175 item 2). Appended last and omitempty to keep the remote wire shape
	// backward-compatible; 0 (no clown) is indistinguishable from an older
	// host that never sends the field, which is correct.
	ClownCount int `json:"clown_count,omitempty"`
}

// ListRows converts states to wire rows with no clown-presence augmentation
// (ClownCount is 0 and State is the PID-only ResolveState). Equivalent to
// ListRowsWithClowns(states, closed, nil); kept for callers that have no
// presence index to consult.
func ListRows(states []State, closed bool) []ListRow {
	return ListRowsWithClowns(states, closed, nil)
}

// ListRowsWithClowns converts states to wire rows, mirroring the text output's
// filter semantics: entries that resolve to abandoned (tombstones, dangling
// symlinks, missing worktrees) are skipped unless closed is true. The abandoned
// filter uses the base ResolveState so presence never un-abandons a row; the
// emitted State is ResolveDisplayState, so a live clown over a dead PID reads
// running-detached rather than inactive (#153). clowns maps a session key to its
// live-clown count (nil ⇒ all zero). The result is never nil so JSON marshaling
// yields [] rather than null.
func ListRowsWithClowns(states []State, closed bool, clowns map[string]int) []ListRow {
	rows := []ListRow{}
	for _, s := range states {
		if s.ResolveState() == StateAbandoned && !closed {
			continue
		}
		n := clowns[s.SessionKey]
		rows = append(rows, ListRow{
			ID:          filepath.Base(s.WorktreePath),
			SessionKey:  s.SessionKey,
			State:       s.ResolveDisplayState(n),
			Description: s.Description,
			Repo:        filepath.Base(s.RepoPath),
			Kind:        s.Kind,
			Branch:      s.Branch,
			SpawnedBy:   s.SpawnedBy,
			ClownCount:  n,
		})
	}
	return rows
}
