// Package safeio is how the frontend writes under a worktree's .spinclass
// directory, which the worker the worktree hosts can write too (FDR 0035). A
// plain os.Create or os.OpenFile follows a symlink the worker planted at the
// final path element, and MkdirAll or CreateTemp follows one planted AS the
// directory, so the frontend would truncate or replace whatever file the planted
// link names. These helpers refuse both.
//
// Residual: a worker that swaps the directory for a symlink between the check
// and the open can still win that race. Frontend state that nothing in the unit
// can see (the session record, the lock) does not depend on these helpers.
package safeio

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// RealDir creates dir (and parents) when absent and refuses it when dir itself
// is a symlink or not a directory.
func RealDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a real directory (mode %s): refusing to write through it", dir, fi.Mode().Type())
	}
	return nil
}

// OpenFile is os.OpenFile that refuses a symlinked parent directory and a
// symlink at the final path element (O_NOFOLLOW).
func OpenFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	if fi, err := os.Lstat(filepath.Dir(path)); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink: refusing to open %s through it", filepath.Dir(path), filepath.Base(path))
	}
	return os.OpenFile(path, flag|syscall.O_NOFOLLOW, perm)
}

// MaxReadSize is the cap ReadRegular callers use for the small per-worktree
// files (the display copy of a session record, the hook sidecar): far above any
// honest one, far below what could exhaust the frontend.
const MaxReadSize = 1 << 20

// ReadRegular reads a file under a worktree the worker can write, the way a
// frontend must: it refuses a symlink at the final element (O_NOFOLLOW, so a link
// to /dev/zero is not followed), never blocks opening a FIFO (O_NONBLOCK), reads
// only a regular file (a FIFO or device that opened is refused by fstat) and no
// more than max bytes (a larger file is an error, not a truncated read). A plain
// os.ReadFile does none of this: a FIFO at the path hangs it, and /dev/zero
// exhausts memory.
func ReadRegular(path string, max int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (mode %s): refusing to read it", path, fi.Mode().Type())
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("%s is %d bytes, over the %d byte limit: refusing to read it", path, fi.Size(), max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s grew past the %d byte limit while being read: refusing it", path, max)
	}
	return data, nil
}

// Create is os.Create through OpenFile.
func Create(path string) (*os.File, error) {
	return OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

// WriteFile is os.WriteFile through OpenFile.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	f, err := OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
