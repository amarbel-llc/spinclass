package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"code.linenisgreat.com/spinclass/internal/0/safeio"
)

// writeAtomic replaces path with data via a temp file in the same directory
// plus rename, so a reader (or a crash) never sees a truncated state file.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// withStateLock runs fn holding an exclusive, blocking flock on lockPath. flock
// is per open file description, so it serializes goroutines in one process as
// well as separate processes. The lock is host-local. A missing directory is
// os.ErrNotExist: the lock never creates one. The open refuses a symlink at
// lockPath (a worker-writable directory could plant one).
func withStateLock(lockPath string, fn func() error) error {
	lf, err := safeio.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", lockPath, err)
	}
	defer func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// Update is a locked read-modify-write of a live worktree session's state:
// it takes the flock that sits beside the frontend record (frontend state, not
// the worker-writable worktree), Reads, calls fn, and writes atomically. An error
// from fn aborts without writing. A missing or closed (tombstoned) session is
// os.ErrNotExist; fn is not called. Every read-modify-write of a live session
// goes through it (#348), so concurrent writers of different fields cannot drop
// each other's update. Write stays for creating a session from scratch and for
// the tombstone and legacy-migration paths.
func Update(repoPath, branch string, fn func(*State) error) error {
	migrateOnce()
	if err := validBranch(branch); err != nil {
		return fmt.Errorf("session.Update: branch %q (%v): %w", branch, err, os.ErrNotExist)
	}
	if err := os.MkdirAll(recordsDir(), 0o700); err != nil {
		return err
	}
	lockPath := recordPath(worktreeFromRepoBranch(repoPath, branch)) + ".lock"
	return withStateLock(lockPath, func() error {
		st, err := Read(repoPath, branch)
		if err != nil {
			return err
		}
		if st.isTombstone {
			return fmt.Errorf("session %s/%s is closed: %w", filepath.Base(repoPath), branch, os.ErrNotExist)
		}
		if err := fn(st); err != nil {
			return err
		}
		return Write(*st)
	})
}

// UpdateImplicit is Update for the implicit (main-checkout) session whose
// state file is <checkout>/.spinclass/state-<randID>.json. It refuses
// (os.ErrNotExist) when that file is gone rather than recreating it.
func UpdateImplicit(checkout, randID string, fn func(*State) error) error {
	statePath := implicitStatePath(checkout, randID)
	return withStateLock(statePath+".lock", func() error {
		data, err := os.ReadFile(statePath)
		if err != nil {
			return err
		}
		var st State
		if err := json.Unmarshal(data, &st); err != nil {
			return err
		}
		if err := fn(&st); err != nil {
			return err
		}
		return WriteImplicit(st, randID)
	})
}
