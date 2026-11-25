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

// errLockHeld is what a non-waiting acquire returns while another holder has
// the lock.
var errLockHeld = errors.New("keyholder: the session lock is held")

// LockPath is the session's lock file. It sits in the keyholder root beside the
// session's directory, never inside it: Stop removes that directory, and a lock
// that lived in it would vanish under whoever held it. A Stop that ends the pair
// removes the lock file too, as its very last step, under the lock itself.
func LockPath(base, sessionKey string) string { return lockPathHash(base, Hash(sessionKey)) }

func lockPathHash(base, hash string) string { return filepath.Join(base, hash+".lock") }

// heldLock is a session's lock while it is held. Unlock releases it; Remove
// deletes the lock file (still holding the lock) and then releases it.
type heldLock struct {
	f    *os.File
	path string
}

// Unlock releases the lock and leaves the file.
func (h *heldLock) Unlock() {
	_ = syscall.Flock(int(h.f.Fd()), syscall.LOCK_UN)
	_ = h.f.Close()
}

// Remove unlinks the lock file while the lock is held, then releases it. The
// file is only unlinked if the path still names the inode this holder locked;
// a waiter that held the old inode sees the mismatch when it gets the flock and
// opens the new file instead, so two holders never overlap across the unlink.
func (h *heldLock) Remove() {
	if sameFile(h.f, h.path) {
		_ = os.Remove(h.path)
	}
	h.Unlock()
}

// sameFile reports whether path still names the file f has open.
func sameFile(f *os.File, path string) bool {
	var a, b syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &a); err != nil {
		return false
	}
	if err := syscall.Lstat(path, &b); err != nil {
		return false
	}
	return a.Dev == b.Dev && a.Ino == b.Ino
}

// lockSession takes the session's exclusive flock and returns its release. Two
// launches of one session (a resume in two terminals, a resume racing a spawn
// retry), or a close racing a resume, are serialised here: without it the loser
// of the unit-name race tears down the winner's live pair on its failure path.
// A flock belongs to the open file description, so two goroutines of one
// process exclude each other too.
func (l *Launcher) lockSession(ctx context.Context, sessionKey string) (func(), error) {
	return l.lockHash(ctx, Hash(sessionKey), sessionKey)
}

// lockHash is lockSession for a pair known by its hash; name is what the
// message calls it.
func (l *Launcher) lockHash(ctx context.Context, hash, name string) (func(), error) {
	h, err := l.acquire(ctx, hash, name, lockWait)
	if err != nil {
		return nil, err
	}
	return h.Unlock, nil
}

// acquire takes the lock for hash, waiting up to wait (zero: try once and return
// errLockHeld when another holder has it). After the flock it checks the path
// still names the file it locked: a Stop that removed the file while this
// caller waited on the old inode leaves it holding a lock nobody else can see,
// so it closes that file and starts again on the new one.
func (l *Launcher) acquire(ctx context.Context, hash, name string, wait time.Duration) (*heldLock, error) {
	if err := os.MkdirAll(filepath.Dir(l.Base), 0o700); err != nil {
		return nil, fmt.Errorf("keyholder: %w", err)
	}
	if err := mkdirExact0700(l.Base); err != nil {
		return nil, fmt.Errorf("keyholder: %w", err)
	}
	path := lockPathHash(l.Base, hash)
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, fmt.Errorf("keyholder: opening the session lock %s: %w", path, err)
		}
		locked := false
		for {
			err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				locked = true
				break
			}
			if !errors.Is(err, syscall.EWOULDBLOCK) {
				_ = f.Close()
				return nil, fmt.Errorf("keyholder: locking %s: %w", path, err)
			}
			if wait == 0 {
				_ = f.Close()
				return nil, errLockHeld
			}
			if ctx.Err() != nil || time.Now().After(deadline) {
				_ = f.Close()
				return nil, fmt.Errorf("keyholder: another start or stop of %s holds %s: try again once it has finished", name, path)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if locked && sameFile(f, path) {
			return &heldLock{f: f, path: path}, nil
		}
		// The file was unlinked (or replaced) while this caller waited: lock the
		// one the path names now.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		if ctx.Err() != nil || (wait > 0 && time.Now().After(deadline)) {
			return nil, fmt.Errorf("keyholder: could not settle the lock %s for %s", path, name)
		}
	}
}
