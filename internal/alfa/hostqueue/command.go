package hostqueue

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Management commands (spinclass#483, plan "Command lifecycle"). The ledger is
// flock-based with no daemon and a waiter's marker is held (and rewritten) by
// the waiter's own process, so a verb cannot edit it. It writes a command file
// `cmd-*.json` under the ledger directory instead; the addressed waiter applies
// it at its next look (at most one poll), under the ledger lock. A command names
// the waiter by place, session, pid and when it began waiting; the waiter
// applies it only if all four match its own marker (the file's inode is not
// used: a waiter rewrites its marker on every reason change). Locked readers
// garbage-collect commands with no live target, so a restarted counter or a
// vanished waiter cannot leave a command that hits a later waiter.
//
// Like the markers, the files are not a security boundary against same-uid code.

const (
	cmdPrefix      = "cmd-"
	cmdMaxAge      = time.Hour
	cmdVerbPri     = "priority"
	cmdVerbHold    = "hold"
	cmdVerbRelease = "release"
	cmdVerbCancel  = "cancel"
)

// Command is one management request addressed to a waiter.
type Command struct {
	Verb         string    `json:"verb"`
	Session      string    `json:"session"`
	Seq          uint64    `json:"seq"`
	PID          int       `json:"pid"`
	WaitingSince time.Time `json:"waiting_since"`
	Priority     string    `json:"priority,omitempty"`
	At           time.Time `json:"at"`
	Actor        string    `json:"actor"`
}

// addresses reports whether c is for the waiter whose marker is m.
func (c Command) addresses(m Marker) bool {
	return c.Seq != 0 && c.Seq == m.Seq && c.Session == m.Session && c.PID == m.PID && c.WaitingSince.Equal(m.WaitingSince)
}

// IsCancel reports whether c ends the waiter's wait.
func (c Command) IsCancel() bool { return c.Verb == cmdVerbCancel }

// Describe is a one-line account of c for the waiter's log.
func (c Command) Describe() string {
	switch c.Verb {
	case cmdVerbPri:
		return fmt.Sprintf("%s set this gate's priority to %s", c.Actor, c.Priority)
	case cmdVerbHold:
		return fmt.Sprintf("%s held this gate (it keeps its place)", c.Actor)
	case cmdVerbRelease:
		return fmt.Sprintf("%s released this gate's hold", c.Actor)
	case cmdVerbCancel:
		return fmt.Sprintf("%s cancelled this gate's wait", c.Actor)
	}
	return fmt.Sprintf("%s sent %s", c.Actor, c.Verb)
}

// SendCommand writes c as a command file, atomically, and returns its path.
func (l Ledger) SendCommand(c Command) (string, error) {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return "", err
	}
	var salt [6]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	final := filepath.Join(l.Dir, fmt.Sprintf("%s%s-%d-%s%s", cmdPrefix, c.Verb, c.At.UnixNano(), hex.EncodeToString(salt[:]), markerSuffix))
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(l.Dir, "tmp-cmd-*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return "", werr
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return final, nil
}

// commandFiles lists the command files with their parsed content; one that
// cannot be parsed is returned with ok false so a locked reader can discard it.
func (l Ledger) commandFiles() (paths []string, cmds []Command, ok []bool) {
	entries, err := os.ReadDir(l.Dir)
	if err != nil {
		return nil, nil, nil
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, cmdPrefix) || !strings.HasSuffix(name, markerSuffix) {
			continue
		}
		path := filepath.Join(l.Dir, name)
		var c Command
		data, rerr := os.ReadFile(path)
		good := rerr == nil && json.Unmarshal(data, &c) == nil
		paths, cmds, ok = append(paths, path), append(cmds, c), append(ok, good)
	}
	return paths, cmds, ok
}

// ApplyCommands applies the commands addressed to this waiter and removes their
// files. Call it under the ledger lock. It returns what it applied, in file
// order (oldest first by name).
func (w *Waiter) ApplyCommands() []Command {
	paths, cmds, ok := w.l.commandFiles()
	var applied []Command
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	if w.p.gone {
		return nil
	}
	for i, c := range cmds {
		if !ok[i] || !c.addresses(w.m) {
			continue
		}
		switch c.Verb {
		case cmdVerbPri:
			if _, err := ParsePriority(c.Priority); err != nil {
				_ = os.Remove(paths[i])
				continue
			}
			w.m.Priority, w.m.PrioritySetAt, w.m.AgeStart = c.Priority, c.At, c.At
			w.p.replaceLocked(w.l, w.m)
		case cmdVerbHold:
			if !w.m.Held {
				w.m.Held, w.m.HeldAt = true, c.At
				w.p.replaceLocked(w.l, w.m)
			}
		case cmdVerbRelease:
			if w.releaseHoldLocked(c.At) {
				w.p.replaceLocked(w.l, w.m)
			}
		case cmdVerbCancel:
			// The marker is not changed: the gate's loop sees the applied command
			// and leaves the line.
		default:
			// A verb this binary does not know: leave it for a newer waiter's
			// binary or the age limit rather than guess.
			continue
		}
		_ = os.Remove(paths[i])
		applied = append(applied, c)
	}
	return applied
}

// releaseHoldLocked ends this waiter's hold at at: the held time is added to
// HeldFor (it does not count against max-wait) and the aging clock moves on by
// it; a priority set during the hold restarts aging at the release instead.
// Reports whether there was a hold. Call with p.mu held.
func (w *Waiter) releaseHoldLocked(at time.Time) bool {
	if !w.m.Held {
		return false
	}
	held := max(at.Sub(w.m.HeldAt), 0)
	w.m.HeldFor += held
	if w.m.PrioritySetAt.After(w.m.HeldAt) {
		w.m.AgeStart = at
	} else {
		base := w.m.AgeStart
		if base.IsZero() {
			base = w.m.WaitingSince
			if w.m.PrioritySetAt.After(base) {
				base = w.m.PrioritySetAt
			}
		}
		w.m.AgeStart = base.Add(held)
	}
	w.m.Held, w.m.HeldAt = false, time.Time{}
	return true
}

// ExpireHold releases the waiter's hold once it has lasted limit, so a forgotten
// hold cannot wedge the line (plan G3). limit 0 means holds never expire. It
// reports whether it released one. Call under the ledger lock.
func (w *Waiter) ExpireHold(now time.Time, limit time.Duration) bool {
	if limit <= 0 {
		return false
	}
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	if w.p.gone || !w.m.Held || now.Sub(w.m.HeldAt) < limit {
		return false
	}
	w.releaseHoldLocked(now)
	w.p.replaceLocked(w.l, w.m)
	return true
}

// CollectCommands removes command files whose target is not among waiting, files
// that cannot be parsed, and files older than an hour. Call it under the ledger
// lock, with the live waiters just read.
func (l Ledger) CollectCommands(waiting []Marker, now time.Time) {
	paths, cmds, ok := l.commandFiles()
	for i, c := range cmds {
		live := false
		if ok[i] {
			for _, m := range waiting {
				if c.addresses(m) {
					live = true
					break
				}
			}
		}
		if !live || (ok[i] && now.Sub(c.At) > cmdMaxAge) {
			_ = os.Remove(paths[i])
		}
	}
}

// CommandPending reports whether the command file at path still exists, that is
// whether its waiter has not applied it (nor a locked reader collected it).
func CommandPending(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ErrNoWaiter is returned when a verb names a session with no waiter.
var ErrNoWaiter = errors.New("no waiting gate")
