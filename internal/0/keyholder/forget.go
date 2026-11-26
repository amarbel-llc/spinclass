package keyholder

import (
	"context"
	"errors"
	"os"
)

// hasLeftovers says whether anything of a session is on record under the state
// root: its key history, its kind record or its lock file. It reads three
// paths and creates nothing, so a host that never used the key holder pays
// three stats.
func (l *Launcher) hasLeftovers(sessionKey string) bool {
	for _, p := range []string{LastPath(l.Base, sessionKey), KindPath(l.Base, sessionKey), lockPathHash(l.Base, Hash(sessionKey))} {
		if _, err := os.Lstat(p); err == nil {
			return true
		}
	}
	return false
}

// ForgetSession ends everything a closed session leaves under the state root
// when it has no pair directory any more: its key history (a stop by the
// operator keeps it, marked stopped, so the next start is announced; a CLOSE
// has nobody left to announce to, and a later session with the same key must not
// be told its first key replaced the old one), its kind record and its lock
// file. It takes the session's lock for it, so it does not race a start. A
// session with nothing on record is not an error and creates nothing.
func (l *Launcher) ForgetSession(ctx context.Context, sessionKey string) error {
	if !l.hasLeftovers(sessionKey) {
		return nil
	}
	h, err := l.acquire(ctx, Hash(sessionKey), sessionKey, lockWait)
	if err != nil {
		return err
	}
	if err := os.Remove(LastPath(l.Base, sessionKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
		h.Unlock()
		return err
	}
	if err := ForgetKind(l.Base, sessionKey); err != nil {
		h.Unlock()
		return err
	}
	h.Remove() // the lock file goes last, under the lock
	l.logf("keyholder: ended the key history of %s (the session is closed)", sessionKey)
	return nil
}
