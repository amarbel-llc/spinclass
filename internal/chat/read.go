package chat

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReadFilter narrows which messages Read returns. The zero value matches
// everything (the full cross-session firehose). Fields combine with AND.
type ReadFilter struct {
	// ToMe limits to messages delivered to the reading session: broadcasts
	// plus direct messages addressed to its session key.
	ToMe bool
	// From limits to messages whose sender is exactly this session key.
	From string
	// Repo limits to messages whose sender is in this repo (the segment
	// before "/" in the sender's `<repo>/<branch>` session key).
	Repo string
}

// matches reports whether m passes the filter for the given reader session
// key. Empty filter fields are no-ops.
func (f ReadFilter) matches(m Message, sessionKey string) bool {
	if f.ToMe && !m.addressedTo(sessionKey) {
		return false
	}
	if f.From != "" && m.From != f.From {
		return false
	}
	if f.Repo != "" && repoOf(m.From) != f.Repo {
		return false
	}
	return true
}

// repoOf returns the repo segment of a session key (`<repo>/<branch>`), i.e.
// everything before the first "/". A key with no "/" is returned unchanged.
func repoOf(sessionKey string) string {
	return strings.SplitN(sessionKey, "/", 2)[0]
}

// cursor is the per-reader read watermark persisted between Read calls.
type cursor struct {
	LastRead time.Time `json:"last_read"`
}

// cursorPath returns the cursor file path for a reader session key. The file
// lives in the chatroom dir, dot-prefixed so entryFilenames skips it.
func cursorPath(sessionKey string) string {
	h := sha256.Sum256([]byte(sessionKey))
	return filepath.Join(Dir(), fmt.Sprintf(".cursor-%x.json", h[:8]))
}

// loadCursor reads the reader's last-read timestamp. A missing or unreadable
// cursor yields the zero time, so every message counts as new.
func loadCursor(sessionKey string) time.Time {
	data, err := os.ReadFile(cursorPath(sessionKey))
	if err != nil {
		return time.Time{}
	}
	var c cursor
	if err := json.Unmarshal(data, &c); err != nil {
		return time.Time{}
	}
	return c.LastRead
}

// saveCursor atomically writes the reader's last-read timestamp.
func saveCursor(sessionKey string, ts time.Time) error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create chatroom dir: %w", err)
	}
	data, err := json.Marshal(cursor{LastRead: ts})
	if err != nil {
		return fmt.Errorf("marshal cursor: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".cursortmp-*.json")
	if err != nil {
		return fmt.Errorf("create temp cursor: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp cursor: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp cursor: %w", err)
	}
	if err := os.Rename(tmpName, cursorPath(sessionKey)); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("commit cursor: %w", err)
	}
	return nil
}

// Read returns messages newer than the reader's cursor that pass the filter,
// in chronological order. sessionKey is the reading session's key (used for
// both the cursor and the ToMe filter).
//
// Cursor semantics are read-through: unless peek is true, the cursor advances
// past every message scanned that is newer than the old cursor — including
// messages the filter excluded — so a later unfiltered Read will not resurface
// them. peek=true performs a non-advancing read (a true preview). The cursor
// only moves forward, never backward.
func Read(sessionKey string, filter ReadFilter, peek bool) ([]Message, error) {
	since := loadCursor(sessionKey)

	names, err := entryFilenames()
	if err != nil {
		return nil, err
	}

	var out []Message
	highWater := since
	for _, name := range names {
		m, err := readMessage(name)
		if err != nil {
			// A message that fails to read (partial write we raced, or a
			// malformed file) is skipped, not fatal — Read must survive one
			// bad entry. It is intentionally not counted toward the
			// watermark, so a later read can retry it once it is whole.
			continue
		}
		if !m.Timestamp.After(since) {
			continue
		}
		if m.Timestamp.After(highWater) {
			highWater = m.Timestamp
		}
		if filter.matches(m, sessionKey) {
			out = append(out, m)
		}
	}

	if !peek && highWater.After(since) {
		if err := saveCursor(sessionKey, highWater); err != nil {
			return out, err
		}
	}
	return out, nil
}
