package keyholder

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// SwitchMarkerName is the file `sc keyholder-switch on` leaves in the key holder's
// state root. It is not a session's file: it carries no hash, so Find, the sweeps,
// `sc keyholder-stop` and `sc close` never see it and never remove it. Its one job
// is to be EVIDENCE: HasAnyState counts it, so once the operator has switched the
// key holder on, a failure to look up the account's home refuses a launch instead
// of warning and proceeding on the operator's agent (the one fail-open cell of the
// switch review, L-6). `sc keyholder-switch off` and `default` remove it.
const SwitchMarkerName = ".switch-on"

// PasswdStateBase is the key holder's state root under the account database's
// home: the root the evidence gate always reads, whatever XDG_STATE_HOME says.
func PasswdStateBase(passwdHome string) string {
	return filepath.Join(passwdHome, ".local", "state", "spinclass", "keyholder")
}

// SwitchMarkerPath is where the marker lives under base.
func SwitchMarkerPath(base string) string { return filepath.Join(base, SwitchMarkerName) }

// WriteSwitchMarker leaves the marker under base: the state root is made a real
// directory of this user with mode exactly 0700 (a link or another user's
// directory is refused), and the file is created 0600, exclusively and never
// through a link. A marker that is already there and ours is left as it is.
func WriteSwitchMarker(base string) error {
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		return err
	}
	if err := mkdirExact0700(base); err != nil {
		return err
	}
	path := SwitchMarkerPath(base)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if errors.Is(err, os.ErrExist) {
		if HasSwitchMarker(base) {
			return nil
		}
		return fmt.Errorf("%s exists and is not the switch marker (a link, another user's file or not a regular file); remove it", path)
	}
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, "on\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// HasSwitchMarker says whether the marker is there: a regular file of this user.
func HasSwitchMarker(base string) bool {
	fi, err := os.Lstat(SwitchMarkerPath(base))
	return err == nil && fi.Mode().IsRegular() && ownedByUs(fi)
}

// RemoveSwitchMarker takes the marker away (an absent one is not an error). A
// link at that name is unlinked, never followed.
func RemoveSwitchMarker(base string) error {
	if err := os.Remove(SwitchMarkerPath(base)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing the switch marker: %w", err)
	}
	return nil
}
