package keyholder

import (
	"errors"
	"os"
)

// HasAnyState reports whether the key holder has ever left anything under base:
// a pair directory, a key history, a kind record or a lock file. It is the
// evidence that the switch is, or once was, on for this host. A base that does
// not exist is "no": a host that never switched the key holder on has none. A
// base that cannot be read is answered "yes" together with the error, because
// not knowing is not evidence of absence (the caller decides what to do with the
// error; the answer is safe to act on).
func HasAnyState(base string) (bool, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return true, err
	}
	return len(entries) > 0, nil
}
