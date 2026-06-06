// Package chat implements a global, open file-backed message store for
// cross-session communication between spinclass sessions. Messages are one
// JSON file each under $XDG_STATE_HOME/spinclass/chatroom/, named by
// timestamp + sender hash for natural lexical ordering. Addressing uses the
// session key (<repo-dirname>/<branch>, == $SPINCLASS_SESSION_ID); a message
// is delivered to a session when its `to` is that key or the broadcast
// sentinel "*".
//
// See docs/features/0009-cross-session-chat-monitor.md (receive path) and
// issue #16 (the chatroom design this is the store for).
package chat

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Broadcast is the `to` sentinel for a message addressed to every session.
const Broadcast = "*"

// Message is one chatroom entry. The on-disk JSON shape is the wire format
// other tools (and the chat-watch monitor) read, so field tags are stable.
// Subject is the short, notification-safe summary (see SubjectMaxLen);
// messages predating it have only Body, and renderers fall back via
// DisplaySubject.
type Message struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Timestamp time.Time `json:"timestamp"`
	Subject   string    `json:"subject,omitempty"`
	Body      string    `json:"body"`
}

// addressedTo reports whether m should be delivered to the session with the
// given key: either a broadcast or a direct message to that key.
func (m Message) addressedTo(sessionKey string) bool {
	return m.To == Broadcast || m.To == sessionKey
}

// xdgStateBase returns $XDG_STATE_HOME or its fallback. Mirrors the
// unexported helper in internal/session so the chatroom lands as a sibling
// of the session index under the same root.
func xdgStateBase() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state")
}

// Dir is the global chatroom directory, a sibling of the session index.
func Dir() string {
	return filepath.Join(xdgStateBase(), "spinclass", "chatroom")
}

// messageFilename derives a lexically-sortable filename for a message:
// <rfc3339nano>-<8-byte sha of from+body>.json. The timestamp prefix gives
// natural ordering; the hash suffix de-collides messages sent in the same
// nanosecond by the same sender. Colons in the timestamp are replaced so the
// name is portable across filesystems.
func messageFilename(m Message) string {
	ts := m.Timestamp.UTC().Format(time.RFC3339Nano)
	ts = strings.ReplaceAll(ts, ":", "")
	h := sha256.Sum256([]byte(m.From + "\x00" + m.Body))
	return fmt.Sprintf("%s-%x.json", ts, h[:8])
}

// Send writes one message file into the chatroom directory. The directory is
// created if absent. The write is atomic (temp file + rename) so a reader
// (the chat-watch monitor) never observes a half-written message.
func Send(m Message) error {
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now().UTC()
	}
	if m.To == "" {
		m.To = Broadcast
	}
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create chatroom dir: %w", err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	final := filepath.Join(dir, messageFilename(m))
	tmp, err := os.CreateTemp(dir, ".tmp-*.json")
	if err != nil {
		return fmt.Errorf("create temp message: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp message: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp message: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("commit message: %w", err)
	}
	return nil
}

// entryFilenames returns the message filenames currently in the chatroom,
// sorted lexically (== chronologically, given the timestamp prefix). Temp
// files (".tmp-*") and non-JSON entries are skipped. A missing directory is
// not an error: it yields an empty slice.
func entryFilenames() ([]string, error) {
	dir := Dir()
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(des))
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// readMessage loads and decodes one message file by name.
func readMessage(name string) (Message, error) {
	var m Message
	data, err := os.ReadFile(filepath.Join(Dir(), name))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	return m, nil
}
