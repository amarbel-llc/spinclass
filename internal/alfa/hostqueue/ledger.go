package hostqueue

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	p, err := l.place(markerPrefix, m)
	if err != nil {
		return nil, err
	}
	return p.remove, nil
}

// Wait records m as a waiter, for host-load and sc queue-status to show; it
// takes no slot and needs no ledger lock. update rewrites its reason; leave
// (idempotent) removes it. A waiter that dies is reaped like a dead gate.
func (l Ledger) Wait(m Marker) (update func(reason string), leave func(), err error) {
	p, err := l.place(waiterPrefix, m)
	if err != nil {
		return nil, nil, err
	}
	update = func(reason string) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.gone || reason == m.Reason {
			return
		}
		m.Reason = reason
		p.replaceLocked(l, m)
	}
	return update, p.remove, nil
}

// placedMarker is a marker in view under final, held by the lock on f.
type placedMarker struct {
	mu    sync.Mutex
	final string
	f     *os.File
	gone  bool
}

// place writes m under prefix, locked before it is renamed into view.
func (l Ledger) place(prefix string, m Marker) (*placedMarker, error) {
	f, err := l.writeLocked(m)
	if err != nil {
		return nil, err
	}
	final := filepath.Join(l.Dir, prefix+strings.TrimPrefix(filepath.Base(f.Name()), "tmp-")+markerSuffix)
	if err := os.Rename(f.Name(), final); err != nil {
		discard(f)
		return nil, err
	}
	return &placedMarker{final: final, f: f}, nil
}

// replaceLocked swaps in a new locked file holding m by an atomic rename, so a
// reader sees the old marker or the new one, never a partly written one; the
// old file's lock is dropped only after the new one is in view. Call it with
// p.mu held. On failure the old marker stays.
func (p *placedMarker) replaceLocked(l Ledger, m Marker) {
	f, err := l.writeLocked(m)
	if err != nil {
		return
	}
	if err := os.Rename(f.Name(), p.final); err != nil {
		discard(f)
		return
	}
	old := p.f
	p.f = f
	_ = syscall.Flock(int(old.Fd()), syscall.LOCK_UN)
	_ = old.Close()
}

// remove takes the marker out of view and drops its lock; it is idempotent.
func (p *placedMarker) remove() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gone {
		return
	}
	p.gone = true
	_ = os.Remove(p.final)
	_ = syscall.Flock(int(p.f.Fd()), syscall.LOCK_UN)
	_ = p.f.Close()
}

// writeLocked writes m to a new temporary file in Dir and returns it locked.
func (l Ledger) writeLocked(m Marker) (*os.File, error) {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(l.Dir, "tmp-*")
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(tmp.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		discard(tmp)
		return nil, err
	}
	data, err := json.Marshal(m)
	if err == nil {
		_, err = tmp.Write(append(data, '\n'))
	}
	if err != nil {
		discard(tmp)
		return nil, err
	}
	return tmp, nil
}

func discard(f *os.File) {
	_ = os.Remove(f.Name())
	_ = f.Close()
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
// a running gate. A waiter's marker is replaced by rename when its reason
// changes, and the old file's lock is then dropped, so a free lock on a file no
// longer at path means "replaced", not "dead": read the path again.
func readMarker(path string, reap bool) (Marker, bool) {
	const replacedRetries = 5
	for range replacedRetries {
		m, alive, replaced := readMarkerOnce(path, reap)
		if !replaced {
			return m, alive
		}
	}
	return Marker{}, false
}

func readMarkerOnce(path string, reap bool) (m Marker, alive, replaced bool) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return Marker{}, false, false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
		if !stillAt(f, path) {
			return Marker{}, false, true
		}
		if reap {
			_ = os.Remove(path)
		}
		return Marker{}, false, false
	}
	// Read through the locked file, not the path: its content was complete
	// before it was renamed into view.
	data, _ := io.ReadAll(f)
	if json.Unmarshal(data, &m) != nil || m.Session == "" {
		m.Session = "(unreadable marker " + filepath.Base(path) + ")"
	}
	return m, true, false
}

func stillAt(f *os.File, path string) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	pi, err := os.Stat(path)
	return err == nil && os.SameFile(fi, pi)
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
