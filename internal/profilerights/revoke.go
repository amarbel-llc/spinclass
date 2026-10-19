package profilerights

import (
	"errors"
	"io/fs"

	"code.linenisgreat.com/spinclass/internal/confinement"
	"code.linenisgreat.com/spinclass/internal/grantstore"
)

// RevokeSession drops the default grant store's binding for sessionKey, tying a
// child's profile right to the session's lifetime (FDR 0034 "Verification and
// revocation"). It is for session teardown (close, clean): it never creates the
// store, so a machine that never enforced rights is untouched, and callers are
// expected to log a returned error rather than fail the teardown.
//
// It also drops the session's frontend principal mapping (FDR 0035), the one
// funnel every session-end path (close, clean, merge teardown) already calls.
func RevokeSession(sessionKey string) error {
	mapErr := removePrincipalMapping(sessionKey)
	dir, err := grantstore.DefaultDir()
	if err != nil {
		return errors.Join(err, mapErr)
	}
	return errors.Join(RevokeSessionIn(dir, sessionKey), mapErr)
}

// removePrincipalMapping deletes the frontend principal mappings that name the
// session, by the mappings' own session key, never the worktree-cached
// principal the session's agent can rewrite.
func removePrincipalMapping(sessionKey string) error {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return err
	}
	return confinement.RemovePrincipalMappingsForSession(stateDir, sessionKey)
}

// RevokeSessionIn is RevokeSession against an explicit store directory.
func RevokeSessionIn(dir, sessionKey string) error {
	store, err := grantstore.OpenExisting(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return store.Unbind(sessionKey)
}
