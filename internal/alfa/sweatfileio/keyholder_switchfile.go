package sweatfileio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The key holder's deploy-free switch (issue #519): a one-word file under the
// ACCOUNT's home (the passwd home, never one the caller's environment names)
// that the operator flips with `sc keyholder-switch`, with no circus merge and no
// deploy in either direction. It is not a sweatfile layer and the host's
// configuration does not generate it: ~/.config/spinclass/sweatfile is the file
// home-manager owns, this is its sibling.
//
//	on       the key holder is on (fibby and piggy still come from the trusted
//	         layers; with no trusted layer supplying them a launch is refused)
//	off      the kill switch: the key holder is off for NEW launches whatever any
//	         layer says (the one exception to "on in any trusted layer wins")
//	default  the layers decide
//
// An absent file is "default". A file that exists and is not exactly one of the
// three words, cannot be read, is a link, is not a regular file or is long is an
// error: a launch is refused rather than guessed. A same-account process can
// write this file; that limit is the one of #515 and is stated where the switch
// is documented.

// KeyholderSwitchValue is the one word the switch file holds.
type KeyholderSwitchValue string

const (
	KeyholderSwitchOn      KeyholderSwitchValue = "on"
	KeyholderSwitchOff     KeyholderSwitchValue = "off"
	KeyholderSwitchDefault KeyholderSwitchValue = "default"
)

// keyholderSwitchMaxBytes bounds the file: three short words and a newline.
const keyholderSwitchMaxBytes = 64

// KeyholderSwitchPath is where the switch file lives under the account's home.
func KeyholderSwitchPath(passwdHome string) string {
	return filepath.Join(passwdHome, ".config", "spinclass", "keyholder-switch")
}

// ParseKeyholderSwitchValue accepts exactly on, off or default, with
// surrounding whitespace (a trailing newline) tolerated.
func ParseKeyholderSwitchValue(s string) (KeyholderSwitchValue, error) {
	switch v := KeyholderSwitchValue(strings.TrimSpace(s)); v {
	case KeyholderSwitchOn, KeyholderSwitchOff, KeyholderSwitchDefault:
		return v, nil
	}
	return "", fmt.Errorf("%q is not one of on, off or default", truncateForError(strings.TrimSpace(s)))
}

func truncateForError(s string) string {
	const max = 40
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// ReadKeyholderSwitch reads the switch file under passwdHome. present is false
// (and the value default) when the file is absent. Any other trouble is an error
// that names the path and the way out, and the caller refuses the launch: a file
// the operator meant as a kill switch must not be guessed at.
func ReadKeyholderSwitch(passwdHome string) (v KeyholderSwitchValue, present bool, err error) {
	if !filepath.IsAbs(passwdHome) {
		return KeyholderSwitchDefault, false, fmt.Errorf("the account's home %q is not an absolute path, so the key holder switch file cannot be located", passwdHome)
	}
	path := KeyholderSwitchPath(passwdHome)
	fi, err := os.Lstat(path)
	// A path component that is a file (ENOTDIR) names no switch file either: a host
	// that never used the key holder must launch as before (L-5 of the switch
	// review).
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return KeyholderSwitchDefault, false, nil
	}
	if err != nil {
		return "", true, switchFileErr(path, err)
	}
	if !fi.Mode().IsRegular() {
		return "", true, switchFileErr(path, fmt.Errorf("it is not a regular file (%s): a link or a directory is never followed", fi.Mode().Type()))
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", true, switchFileErr(path, err)
	}
	defer f.Close()
	fi2, err := f.Stat()
	if err != nil {
		return "", true, switchFileErr(path, err)
	}
	if !fi2.Mode().IsRegular() {
		return "", true, switchFileErr(path, errors.New("it is not a regular file"))
	}
	// The writer checks owner and mode; so does the reader (L-7): a file of another
	// user, or one group or other can write, is not the operator's word.
	if st, ok := fi2.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return "", true, switchFileErr(path, fmt.Errorf("it is owned by uid %d, not by this account (uid %d)", st.Uid, os.Getuid()))
	}
	if fi2.Mode().Perm()&0o022 != 0 {
		return "", true, switchFileErr(path, fmt.Errorf("it is writable by group or other (%s): `chmod go-w` it", fi2.Mode().Perm()))
	}
	if fi2.Size() > keyholderSwitchMaxBytes {
		return "", true, switchFileErr(path, fmt.Errorf("it holds %d bytes; it holds one word", fi2.Size()))
	}
	b, err := io.ReadAll(io.LimitReader(f, keyholderSwitchMaxBytes+1))
	if err != nil {
		return "", true, switchFileErr(path, err)
	}
	if len(b) > keyholderSwitchMaxBytes {
		return "", true, switchFileErr(path, errors.New("it holds more than one word"))
	}
	v, perr := ParseKeyholderSwitchValue(string(b))
	if perr != nil {
		return "", true, switchFileErr(path, perr)
	}
	return v, true, nil
}

func switchFileErr(path string, err error) error {
	return fmt.Errorf("the key holder switch file %s: %w: launches are refused until it is fixed: run `sc keyholder-switch default` at a terminal, or remove the file", path, err)
}

// WriteKeyholderSwitch sets the switch file under passwdHome: the directory is
// created 0700 when missing and checked (a real directory, owned by the caller,
// not writable by group or other); the word is written to a temporary file
// opened O_EXCL|O_NOFOLLOW, mode 0600, then renamed over the target in the same
// directory; a target that is a link or not a regular file is refused, never
// followed or replaced blindly.
func WriteKeyholderSwitch(passwdHome string, v KeyholderSwitchValue) error {
	if _, err := ParseKeyholderSwitchValue(string(v)); err != nil {
		return err
	}
	if !filepath.IsAbs(passwdHome) {
		return fmt.Errorf("the account's home %q is not an absolute path", passwdHome)
	}
	path := KeyholderSwitchPath(passwdHome)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	di, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !di.IsDir() {
		return fmt.Errorf("%s is not a real directory (%s): a link is never followed", dir, di.Mode().Type())
	}
	if st, ok := di.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by uid %d, not by this account (uid %d)", dir, st.Uid, os.Getuid())
	}
	if di.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or other (%s): the key holder switch file would not be the operator's alone; run `chmod go-w %s`", dir, di.Mode().Perm(), dir)
	}
	if ti, err := os.Lstat(path); err == nil && !ti.Mode().IsRegular() {
		return fmt.Errorf("%s exists and is not a regular file (%s): remove it by hand; a link is never followed or replaced", path, ti.Mode().Type())
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A random name (O_EXCL, 0600): a leftover of a killed run or a file another
	// process pre-created cannot make the command fail with a bare EEXIST (L-8).
	f, err := os.CreateTemp(dir, ".keyholder-switch.*.tmp")
	if err != nil {
		return fmt.Errorf("creating the temporary switch file: %w", err)
	}
	tmp := f.Name()
	_, werr := f.WriteString(string(v) + "\n")
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing the switch file: %w", werr)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	// Make the rename itself durable: after a power cut the old word must not come
	// back (best effort: a directory that cannot be synced does not undo the write).
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
