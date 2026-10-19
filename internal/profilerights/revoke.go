package profilerights

import (
	"errors"
	"io/fs"

	"code.linenisgreat.com/spinclass/internal/grantstore"
)

// RevokeSession drops the default grant store's binding for sessionKey, tying a
// child's profile right to the session's lifetime (FDR 0034 "Verification and
// revocation"). It is for session teardown (close, clean): it never creates the
// store, so a machine that never enforced rights is untouched, and callers are
// expected to log a returned error rather than fail the teardown.
func RevokeSession(sessionKey string) error {
	dir, err := grantstore.DefaultDir()
	if err != nil {
		return err
	}
	return RevokeSessionIn(dir, sessionKey)
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
