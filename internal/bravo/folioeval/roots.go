package folioeval

import (
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// RootsFor builds the roots for a session: worktree is the session's
// worktree (the pwd anchor too), repoRoot the main checkout, home the
// evaluating process's HOME, principal the caller's CLOWN_SESSION_ID, and
// states the session index the held children are read from. Every root is
// symlink-resolved so a resolved path compares by prefix. An empty principal
// holds nothing (FDR 0032: an unresolvable caller never falls through to
// authorized).
func RootsFor(worktree, repoRoot, home, principal string, env func(string) string, states []session.State) Roots {
	r := Roots{
		Pwd:      Resolve(worktree),
		Home:     Resolve(home),
		NixStore: "/nix/store",
		Env: func(name string) string {
			if env == nil {
				return ""
			}
			if v := env(name); v != "" && filepath.IsAbs(v) {
				return Resolve(v)
			}
			return ""
		},
		Worktree: Resolve(worktree),
	}
	if repoRoot != "" {
		r.Repo = Resolve(repoRoot)
		r.Worktrees = filepath.Join(r.Repo, ".worktrees")
		r.SiblingsParent = filepath.Dir(r.Repo)
		r.SiblingsExclude = r.Repo
	}
	if principal == "" {
		return r
	}
	for _, st := range states {
		if st.IsTombstone() || st.Kind == session.KindImplicit || st.WorktreePath == "" || !st.IsHolder(principal) {
			continue
		}
		r.Children = append(r.Children, Resolve(st.WorktreePath))
	}
	return r
}

// Resolve is the path rule patterns are matched against: the path's parent
// is symlink-resolved (walking up to the nearest existing ancestor when it
// does not exist) and the leaf is kept as named, so a `result` link into
// /nix/store inside a worktree still counts as a worktree path, while a
// worktree reached through a linked parent directory resolves to where it
// is. A relative path is cleaned only.
func Resolve(path string) string {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return cleaned
	}
	parent, leaf := filepath.Dir(cleaned), filepath.Base(cleaned)
	if parent == cleaned {
		return cleaned
	}
	return filepath.Join(resolveExisting(parent), leaf)
}

// resolveExisting resolves symlinks on path, or on its nearest existing
// ancestor with the rest re-appended.
func resolveExisting(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	parent, leaf := filepath.Dir(path), filepath.Base(path)
	if parent == path {
		return path
	}
	return filepath.Join(resolveExisting(parent), leaf)
}
