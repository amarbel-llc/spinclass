package keyholder

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The two kinds of session the key holder tells apart. A DRIVER is a session the
// operator started himself at his terminal (rule A: it keeps his agent and gets
// no pair); everything else is a WORKER. The kind belongs to the SESSION, not to
// the process that happens to invoke it, so it is written down once, at the first
// launch with the switch on, and read back afterwards (B-M3).
const (
	KindDriver = "driver"
	KindWorker = "worker"
)

// KindPath is the one-line file that records a session's kind under base. It is
// kept beside the key history (`<hash>.last.json`), not inside the pair directory,
// because a driver has no pair directory and the record must outlive a lost pair.
func KindPath(base, sessionKey string) string {
	return filepath.Join(base, Hash(sessionKey)+".kind")
}

// RecordKind writes the session's kind if none is recorded, and returns the kind
// on record afterwards: the first writer wins, so a later launch that classifies
// the same session differently cannot change what it is. kind must be KindDriver
// or KindWorker. The file is 0600, created exclusively and never through a link.
func RecordKind(base, sessionKey, kind string) (string, error) {
	if kind != KindDriver && kind != KindWorker {
		return "", fmt.Errorf("keyholder: %q is not a session kind", kind)
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		return "", err
	}
	if err := mkdirExact0700(base); err != nil {
		return "", err
	}
	path := KindPath(base, sessionKey)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if errors.Is(err, os.ErrExist) {
		if k, ok := ReadKind(base, sessionKey); ok {
			return k, nil
		}
		return "", fmt.Errorf("keyholder: %s exists but does not name a session kind; remove it", path)
	}
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(f, kind+"\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return kind, nil
}

// ReadKind returns the recorded kind of a session, and false when there is none
// (a session first launched before this record existed, or with the switch off) or
// the file is not a recognisable kind. A link, a file that is not ours, and
// anything over a few bytes are not read.
func ReadKind(base, sessionKey string) (string, bool) {
	path := KindPath(base, sessionKey)
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 16 || !ownedByUs(fi) {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	switch k := strings.TrimSpace(string(b)); k {
	case KindDriver, KindWorker:
		return k, true
	}
	return "", false
}

// ForgetKind removes a session's kind record: the session is closed, or its
// pair's history is ended. A session with none is not an error.
func ForgetKind(base, sessionKey string) error {
	if err := os.Remove(KindPath(base, sessionKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing the kind record of %s: %w", sessionKey, err)
	}
	return nil
}
