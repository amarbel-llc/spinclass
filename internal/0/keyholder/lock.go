package keyholder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockWait bounds how long a Start or Stop waits for another one of the same
// session to finish; a var so a test can shorten it.
var lockWait = 60 * time.Second

// LockPath is the session's lock file. It sits in the keyholder root beside the
// session's directory, never inside it: Stop removes that directory, and a lock
// that lived in it would vanish under whoever held it.
func LockPath(base, sessionKey string) string {
	return filepath.Join(base, Hash(sessionKey)+".lock")
}

// lockSession takes the session's exclusive flock and returns its release. Two
// launches of one session (a resume in two terminals, a resume racing a spawn
// retry), or a close racing a resume, are serialised here: without it the loser
// of the unit-name race tears down the winner's live pair on its failure path.
// A flock belongs to the open file description, so two goroutines of one
// process exclude each other too.
func (l *Launcher) lockSession(ctx context.Context, sessionKey string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(l.Base), 0o700); err != nil {
		return nil, fmt.Errorf("keyholder: %w", err)
	}
	if err := mkdirExact0700(l.Base); err != nil {
		return nil, fmt.Errorf("keyholder: %w", err)
	}
	path := LockPath(l.Base, sessionKey)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("keyholder: opening the session lock %s: %w", path, err)
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("keyholder: locking %s: %w", path, err)
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("keyholder: another start or stop of %s holds %s: try again once it has finished", sessionKey, path)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
