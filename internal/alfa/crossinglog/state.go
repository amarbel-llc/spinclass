package crossinglog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// State is what a session's bridge frontend last reported about its listener:
// the file `sc list` and session-health read, so a bridge that is down is seen
// by the operator without anyone reading a serve log. It carries a reason in
// bounded, control-free text and nothing a peer sent.
type State struct {
	Version   int    `json:"version"`
	Session   string `json:"session"`
	Listening bool   `json:"listening"`
	// Reason says why the listener is not up (empty while listening).
	Reason string `json:"reason,omitempty"`
	// Since is when Listening last changed.
	Since     time.Time `json:"since"`
	UpdatedAt time.Time `json:"updated_at"`
}

const (
	// stateVersion is the only version written or read.
	stateVersion = 1
	// maxStateReason bounds the reason text; maxStateBytes bounds what is read.
	maxStateReason = 300
	maxStateBytes  = 8 << 10
)

// Note is the short line `sc list` and session-health show: empty when the
// state is absent or the listener is up and so nothing needs saying.
func (s State) Note() string {
	if s.Listening {
		return ""
	}
	if s.Reason == "" {
		return "bridge DOWN"
	}
	return "bridge DOWN: " + s.Reason
}

// SafeReason bounds text for State.Reason: control and format characters become
// spaces, runs of space collapse, and the result is cut to maxStateReason runes.
func SafeReason(s string) string {
	s = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) || c == ' ' || c == ' ' {
			return ' '
		}
		return c
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxStateReason {
		s = string(r[:maxStateReason]) + "..."
	}
	return s
}

// WriteState writes st atomically (a temp file in dir, then a rename) as
// <principal>.state.json, 0600, creating dir 0700. A write that fails is an
// error the caller logs; a state file is never worth failing a serve over.
func WriteState(dir, principal string, st State) error {
	if principal == "" {
		return errors.New("bridge state: no principal to name the file")
	}
	st.Version = stateVersion
	st.Reason = SafeReason(st.Reason)
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	final := StatePath(dir, principal)
	f, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ReadState reads a principal's state file. ok is false when there is none or
// it is not a file this uid wrote in the shape written: the file is opened
// without following a link and must be a small regular file of this uid.
func ReadState(dir, principal string) (State, bool) {
	f, err := os.OpenFile(StatePath(dir, principal), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return State{}, false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return State{}, false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return State{}, false
	}
	b, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if err != nil || len(b) > maxStateBytes {
		return State{}, false
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil || s.Version != stateVersion {
		return State{}, false
	}
	s.Reason = SafeReason(s.Reason)
	return s, true
}

// StateDir is the directory a state home keeps the bridge files in.
func StateDir(stateHome string) string {
	return filepath.Join(stateHome, "spinclass", "bridge")
}

// String renders a state for a log line.
func (s State) String() string {
	if s.Listening {
		return "listening"
	}
	return fmt.Sprintf("down (%s)", s.Reason)
}
