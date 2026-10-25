package session

// KeyAtWorktree is the key of the live, non-implicit session whose worktree root
// is root, from frontend records (the grit store tools' store.Session.SessionAt).
func KeyAtWorktree(root string) (string, bool) {
	st, err := FindByWorktreePath(root)
	if err != nil || st.IsTombstone() || st.Kind == KindImplicit || st.WorktreePath == "" {
		return "", false
	}
	return st.Key(), true
}

// HeldKeys is the keys of the sessions principal holds an accepted handle on
// (FDR 0032). An empty principal holds none.
func HeldKeys(principal string) []string {
	if principal == "" {
		return nil
	}
	all, err := ListAll(nil)
	if err != nil {
		return nil
	}
	var keys []string
	for _, st := range all {
		if st.IsHolder(principal) {
			keys = append(keys, st.Key())
		}
	}
	return keys
}
