// Package mergelock implements a host-local, per-repo advisory merge lock
// (spinclass#235). Merges for a repo serialize on this lock so concurrent
// sessions' merges drain in order instead of racing the ff-only merge.
//
// Design notes:
//
//   - The lock is an advisory flock(2) (syscall.Flock, stdlib — available on
//     both linux and darwin) taken on a well-known file
//     ("spinclass-merge.lock") inside the repo's git common dir, which every
//     worktree of a repo shares. Advisory is sufficient: only spinclass
//     merges participate.
//
//   - flock is per open-file-description and self-releases when the holding
//     process dies (the kernel drops the lock when the last fd referencing
//     the description closes), so there is no stale-lock state to detect or
//     repair.
//
//   - Acquisition polls with LOCK_EX|LOCK_NB rather than issuing a blocking
//     LOCK_EX. A blocking flock call parks the goroutine's thread in the
//     kernel with no way to abandon the wait when the caller's context is
//     cancelled, and interruption semantics differ across platforms; a
//     short-interval non-blocking poll keeps acquisition context-cancellable
//     and portable at the cost of sub-second latency, which is negligible
//     against merge durations.
//
//   - The lock file is never unlinked. If a holder unlinked it on release
//     while a waiter still held an fd to the old inode, a subsequent
//     acquirer would open (and lock) a fresh inode at the same path, and two
//     processes would each "hold" the lock on different inodes. Leaving the
//     empty file in the git common dir is harmless.
//
//   - The file's contents are a best-effort identity hint: the holder writes
//     its ID after acquiring so waiters can report who they are queued
//     behind. The contents carry no locking semantics.
package mergelock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// LockFileName is the name of the lock file inside the git common dir.
const LockFileName = "spinclass-merge.lock"

// Poll and notification intervals. Package-level vars so tests can shorten
// them; production callers should leave them alone.
var (
	// pollInterval is how often Acquire retries LOCK_EX|LOCK_NB while
	// waiting for the lock.
	pollInterval = 500 * time.Millisecond

	// waitNotifyInterval is how often onWait is re-invoked while still
	// waiting (after the immediate first call).
	waitNotifyInterval = 30 * time.Second
)

// Lock is a held merge lock. Release it exactly once when the merge
// finishes; Release is idempotent so a deferred second call is safe.
type Lock struct {
	mu       sync.Mutex
	file     *os.File
	released bool
}

// Acquire takes the per-repo merge lock for the repo whose git common dir is
// gitCommonDir (an absolute path). holderID is written into the lock file
// after acquisition so that waiters can name who they are queued behind.
//
// If the lock is free it is acquired immediately. Otherwise Acquire polls
// (LOCK_EX|LOCK_NB) until the lock is acquired or ctx is done, in which case
// it returns ctx.Err().
//
// onWait may be nil. When non-nil it is called once immediately upon
// entering the wait state and then every waitNotifyInterval while still
// waiting, with the current holder's identity (best-effort read of the lock
// file; empty if unreadable or empty) and the elapsed wait time.
func Acquire(ctx context.Context, gitCommonDir, holderID string, onWait func(holder string, elapsed time.Duration)) (*Lock, error) {
	path := filepath.Join(gitCommonDir, LockFileName)

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening merge lock file: %w", err)
	}

	acquired, err := tryFlock(file)
	if err != nil {
		file.Close()
		return nil, err
	}

	if !acquired {
		if err := waitFlock(ctx, file, onWait); err != nil {
			file.Close()
			return nil, err
		}
	}

	writeHolder(file, holderID)

	return &Lock{file: file}, nil
}

// waitFlock polls for the lock until acquired or ctx is done.
func waitFlock(ctx context.Context, file *os.File, onWait func(holder string, elapsed time.Duration)) error {
	start := time.Now()

	notify := func() {
		if onWait != nil {
			onWait(readHolder(file), time.Since(start))
		}
	}
	notify()
	lastNotify := time.Now()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		acquired, err := tryFlock(file)
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}

		if time.Since(lastNotify) >= waitNotifyInterval {
			notify()
			lastNotify = time.Now()
		}
	}
}

// tryFlock attempts a non-blocking exclusive flock on file. It reports
// whether the lock was acquired; a would-block condition is (false, nil).
func tryFlock(file *os.File) (bool, error) {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return false, nil
		default:
			return false, fmt.Errorf("flock: %w", err)
		}
	}
}

// readHolder best-effort reads the current holder identity from the lock
// file. Returns "" on any error or empty contents.
func readHolder(file *os.File) string {
	buf := make([]byte, 256)
	n, err := file.ReadAt(buf, 0)
	if n == 0 && err != nil {
		return ""
	}
	return string(bytes.TrimSpace(buf[:n]))
}

// writeHolder best-effort records holderID as the lock file's contents.
func writeHolder(file *os.File, holderID string) {
	if err := file.Truncate(0); err != nil {
		return
	}
	_, _ = file.WriteAt([]byte(holderID), 0)
}

// Release drops the lock: best-effort truncates the identity, then unlocks
// and closes the file. Idempotent — a second Release is a no-op returning
// nil. The lock file itself is deliberately never unlinked (see the package
// doc).
func (l *Lock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.released {
		return nil
	}
	l.released = true

	// Best-effort: clear the identity so a stale holder name is not
	// reported after release.
	_ = l.file.Truncate(0)

	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()

	if unlockErr != nil {
		return fmt.Errorf("unlocking merge lock: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing merge lock file: %w", closeErr)
	}
	return nil
}
