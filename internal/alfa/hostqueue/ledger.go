package hostqueue

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Marker is one admitted gate's record in the ledger.
type Marker struct {
	Session    string    `json:"session"`
	Repo       string    `json:"repo"`
	Kind       string    `json:"kind"` // merge, check, guarded
	AdmittedAt time.Time `json:"admitted_at"`
	PID        int       `json:"pid"`
	// A waiter's marker (Wait) carries when it began waiting and why it waits.
	WaitingSince time.Time `json:"waiting_since"`
	Reason       string    `json:"reason,omitempty"`
}

// Ledger is the on-disk queue state. Dir holds the live markers and the ledger
// lock; it belongs under $XDG_RUNTIME_DIR (cleared at boot). HoldPath is the
// operator's hold file; it belongs under $XDG_STATE_HOME (it survives a
// reboot, as an operator's hold should).
type Ledger struct {
	Dir      string
	HoldPath string
}

const (
	lockName     = "ledger.lock"
	markerPrefix = "gate-"
	waiterPrefix = "wait-"
	markerSuffix = ".json"
)

// DefaultLedger is the ledger at its ruled locations (operator, 2026-11-10 Q3).
func DefaultLedger() (Ledger, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return Ledger{}, errors.New("XDG_RUNTIME_DIR is not set, so there is no per-boot place for the host queue's ledger")
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Ledger{}, err
		}
		state = filepath.Join(home, ".local", "state")
	}
	return Ledger{
		Dir:      filepath.Join(runtime, "spinclass", "host-queue"),
		HoldPath: filepath.Join(state, "spinclass", "host-queue", "hold"),
	}, nil
}

// Lock takes the ledger lock, blocking. Every admission decision and every
// reap runs under it; it is held for a probe and a write, never for a gate.
func (l Ledger) Lock() (unlock func(), err error) {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(l.Dir, lockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// Admit writes m as a live marker and holds it until release. The marker is
// locked before it becomes visible under its final name, so a reader never
// sees an unlocked live marker. Call it under Lock.
func (l Ledger) Admit(m Marker) (release func(), err error) {
	_, release, err = l.place(markerPrefix, m)
	return release, err
}

// Wait records m as a waiter, for host-load and sc queue-status to show; it
// takes no slot and needs no ledger lock. update rewrites its reason; leave
// (idempotent) removes it. A waiter that dies is reaped like a dead gate.
func (l Ledger) Wait(m Marker) (update func(reason string), leave func(), err error) {
	f, leave, err := l.place(waiterPrefix, m)
	if err != nil {
		return nil, nil, err
	}
	var mu sync.Mutex
	update = func(reason string) {
		mu.Lock()
		defer mu.Unlock()
		m.Reason = reason
		if data, err := json.Marshal(m); err == nil {
			_ = f.Truncate(0)
			_, _ = f.WriteAt(append(data, '\n'), 0)
		}
	}
	return update, leave, nil
}

// place writes m under prefix, locked before it is renamed into view, and
// returns the open file (which holds the lock) and its idempotent removal.
func (l Ledger) place(prefix string, m Marker) (*os.File, func(), error) {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return nil, nil, err
	}
	tmp, err := os.CreateTemp(l.Dir, "tmp-*")
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*os.File, func(), error) {
		_ = os.Remove(tmp.Name())
		_ = tmp.Close()
		return nil, nil, err
	}
	if err := syscall.Flock(int(tmp.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return fail(err)
	}
	final := filepath.Join(l.Dir, prefix+strings.TrimPrefix(filepath.Base(tmp.Name()), "tmp-")+markerSuffix)
	if err := os.Rename(tmp.Name(), final); err != nil {
		return fail(err)
	}
	var once sync.Once
	return tmp, func() {
		once.Do(func() {
			_ = os.Remove(final)
			_ = syscall.Flock(int(tmp.Fd()), syscall.LOCK_UN)
			_ = tmp.Close()
		})
	}, nil
}

// Live lists the live markers, oldest admission first. With reap, a marker
// whose lock can be taken (its holder is gone) is removed; reap only under
// Lock. Without it (a read-only view, such as host-load's), a dead marker is
// skipped and left for the next locked reader.
func (l Ledger) Live(reap bool) ([]Marker, error) {
	live, err := l.markers(markerPrefix, reap)
	sort.Slice(live, func(i, j int) bool { return live[i].AdmittedAt.Before(live[j].AdmittedAt) })
	return live, err
}

// Waiting lists the live waiters, longest-waiting first; reap as for Live.
func (l Ledger) Waiting(reap bool) ([]Marker, error) {
	waiting, err := l.markers(waiterPrefix, reap)
	sort.Slice(waiting, func(i, j int) bool { return waiting[i].WaitingSince.Before(waiting[j].WaitingSince) })
	return waiting, err
}

func (l Ledger) markers(prefix string, reap bool) ([]Marker, error) {
	entries, err := os.ReadDir(l.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var live []Marker
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, markerSuffix) {
			continue
		}
		path := filepath.Join(l.Dir, name)
		m, alive := readMarker(path, reap)
		if alive {
			live = append(live, m)
		}
	}
	return live, nil
}

// readMarker reads one marker and probes its lock. A marker it cannot parse is
// still counted when its lock is held: a live holder with an odd file is still
// a running gate.
func readMarker(path string, reap bool) (Marker, bool) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return Marker{}, false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		if reap {
			_ = os.Remove(path)
		}
		return Marker{}, false
	}
	var m Marker
	data, _ := os.ReadFile(path)
	if json.Unmarshal(data, &m) != nil || m.Session == "" {
		m.Session = "(unreadable marker " + filepath.Base(path) + ")"
	}
	return m, true
}

// Hold reports the operator's hold and its reason.
func (l Ledger) Hold() (held bool, reason string) {
	data, err := os.ReadFile(l.HoldPath)
	if err != nil {
		return false, ""
	}
	return true, strings.TrimSpace(string(data))
}

// SetHold places the operator's hold: nothing new is admitted until it is
// cleared; running gates continue.
func (l Ledger) SetHold(reason string) error {
	if err := os.MkdirAll(filepath.Dir(l.HoldPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(l.HoldPath, []byte(strings.TrimSpace(reason)+"\n"), 0o600)
}

// ClearHold removes the hold; clearing an absent hold is not an error.
func (l Ledger) ClearHold() error {
	if err := os.Remove(l.HoldPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clearing the host queue hold: %w", err)
	}
	return nil
}

// View reads the ledger for the predicate: live markers (reaped when reap) and
// the hold.
func (l Ledger) View(reap bool) (View, error) {
	live, err := l.Live(reap)
	if err != nil {
		return View{}, err
	}
	waiting, err := l.Waiting(reap)
	if err != nil {
		return View{}, err
	}
	held, reason := l.Hold()
	return View{Live: live, Waiting: waiting, Held: held, HoldReason: reason}, nil
}
