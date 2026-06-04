package chat

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GCMessages removes chatroom message files whose timestamp is older than
// retention, plus orphaned cursor files (`.cursor-*.json`) whose mtime is
// older than retention. retention <= 0 is a no-op (nothing expires). Returns
// the count of files removed. Mirrors session.GCTombstones.
//
// Cursor files are keyed by an irreversible sha8(sessionKey), so they cannot
// be matched back to the session index; mtime-older-than-retention is the
// robust proxy for "no reader has touched this in a long time." A live reader
// rewrites its cursor on every Read, keeping it fresh.
func GCMessages(retention time.Duration) (int, error) {
	if retention <= 0 {
		return 0, nil
	}

	dir := Dir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	cutoff := time.Now().Add(-retention)
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()

		switch {
		case isCursorFile(name):
			info, ierr := e.Info()
			if ierr != nil || !info.ModTime().Before(cutoff) {
				continue
			}
		case isMessageFile(name):
			m, rerr := readMessage(name)
			if rerr != nil || !m.Timestamp.Before(cutoff) {
				continue
			}
		default:
			// Other entries (in-flight temp files etc.) are out of scope.
			continue
		}

		if os.Remove(filepath.Join(dir, name)) == nil {
			removed++
		}
	}
	return removed, nil
}

// CountStaleMessages returns how many message and cursor files would be
// removed by GCMessages(retention) without removing them. retention <= 0
// returns 0. Mirrors clean.countStaleTombstones for the dry-run plan.
func CountStaleMessages(retention time.Duration) int {
	if retention <= 0 {
		return 0
	}
	dir := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-retention)
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case isCursorFile(name):
			info, ierr := e.Info()
			if ierr == nil && info.ModTime().Before(cutoff) {
				count++
			}
		case isMessageFile(name):
			if m, rerr := readMessage(name); rerr == nil && m.Timestamp.Before(cutoff) {
				count++
			}
		}
	}
	return count
}

// isMessageFile reports whether name is a committed message file (the shape
// messageFilename produces): a non-dot ".json" entry.
func isMessageFile(name string) bool {
	return !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".json")
}

// isCursorFile reports whether name is a per-reader cursor file.
func isCursorFile(name string) bool {
	return strings.HasPrefix(name, ".cursor-") && strings.HasSuffix(name, ".json")
}
