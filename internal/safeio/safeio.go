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
