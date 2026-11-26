package keyholder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// UnitLister is the optional half of a Runner that can say which key holder
// units exist (loaded, in any state). SystemdRunner has it; a fake that does not
// is simply never asked.
type UnitLister interface {
	// ListUnits returns the names of the loaded units that match the pair naming
	// scheme (spinclass-keyholder-*).
	ListUnits(ctx context.Context) ([]string, error)
}

var (
	hashRE = regexp.MustCompile(`^[0-9a-f]{16}$`)
	unitRE = regexp.MustCompile(`^spinclass-keyholder-(?:fibby|agent)-([0-9a-f]{16})\.service$`)
)

// Found is a pair, or what is left of one, discovered by Find: a directory under
// the keyholder root, loaded units named for the same hash, or both. The session
// key is known only from a readable record.
type Found struct {
	Hash    string
	Session string // from the record; "" when the directory has no readable record
	Dir     string // the session directory when it exists
	Units   []string
	Running bool // either unit is active
	// Age is how long ago the pair was minted (its record's time, else the
	// directory's); zero when there is neither.
	Age time.Duration
	// LockOnly is true when all that is left of the pair is its `<hash>.lock`
	// file: no directory, no loaded unit. StopFound removes such a file only if
	// no process holds the lock, and never waits for one.
	LockOnly bool
	// Unowned is true when systemd lists units for this hash but THIS state root
	// recorded nothing for it: no directory, no key history, no lock file. Units
	// are per user and the state root follows the caller's XDG_STATE_HOME, so a
	// process that runs under another state root sees every pair of the account as
	// unowned. Such units are somebody else's pairs, not leftovers: they are
	// reported and never stopped, unless the operator names them with
	// StopOptions.IncludeUnowned.
	Unowned bool
}

// Find lists every pair under the root and every key holder unit systemd has
// loaded, one Found per hash, sorted by hash. It starts and stops nothing.
func (l *Launcher) Find(ctx context.Context) ([]Found, error) {
	byHash := map[string]*Found{}
	get := func(h string) *Found {
		f, ok := byHash[h]
		if !ok {
			f = &Found{Hash: h}
			byHash[h] = f
		}
		return f
	}
	entries, err := os.ReadDir(l.Base)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("keyholder: reading %s: %w", l.Base, err)
	}
	// What this state root recorded, by name: the directories, the key histories
	// and the lock files are the trace a pair of THIS root leaves.
	recorded := map[string]bool{}
	now := time.Now()
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() && hashRE.MatchString(name):
			recorded[name] = true
		case strings.HasSuffix(name, ".last.json") && hashRE.MatchString(strings.TrimSuffix(name, ".last.json")):
			recorded[strings.TrimSuffix(name, ".last.json")] = true
		case strings.HasSuffix(name, ".lock") && hashRE.MatchString(strings.TrimSuffix(name, ".lock")):
			recorded[strings.TrimSuffix(name, ".lock")] = true
		case strings.HasSuffix(name, ".kind") && hashRE.MatchString(strings.TrimSuffix(name, ".kind")):
			recorded[strings.TrimSuffix(name, ".kind")] = true
		}
		if !e.IsDir() || !hashRE.MatchString(name) {
			continue
		}
		f := get(name)
		f.Dir = filepath.Join(l.Base, name)
		if rec, ok := readRecord(filepath.Join(f.Dir, "record.json")); ok {
			f.Session = rec.Session
			if !rec.MintedAt.IsZero() {
				f.Age = now.Sub(rec.MintedAt)
			}
		}
		if f.Age == 0 {
			if fi, err := e.Info(); err == nil {
				f.Age = now.Sub(fi.ModTime())
			}
		}
	}
	if lister, ok := l.Run.(UnitLister); ok {
		units, err := lister.ListUnits(ctx)
		if err != nil {
			return nil, err
		}
		for _, u := range units {
			if m := unitRE.FindStringSubmatch(u); m != nil {
				f := get(m[1])
				f.Units = append(f.Units, u)
			}
		}
	}
	// A `<hash>.lock` with no directory and no unit is all that is left of a pair
	// whose stop was cut short (or of a build that left it behind).
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".lock") || !hashRE.MatchString(strings.TrimSuffix(name, ".lock")) {
			continue
		}
		h := strings.TrimSuffix(name, ".lock")
		if _, known := byHash[h]; known {
			continue
		}
		f := get(h)
		f.LockOnly = true
		if fi, err := e.Info(); err == nil {
			f.Age = now.Sub(fi.ModTime())
		}
	}
	out := make([]Found, 0, len(byHash))
	for h, f := range byHash {
		sort.Strings(f.Units)
		f.Unowned = !recorded[h]
		ly := l.layoutForHash(h)
		for _, u := range []string{ly.fibbyUnit, ly.agentUnit} {
			if act, err := l.Run.Active(ctx, u); err == nil && act {
				f.Running = true
			}
		}
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hash < out[j].Hash })
	return out, nil
}

// StopOptions says how StopFoundWith stops a pair.
type StopOptions struct {
	// KeepHistory keeps the session's key history and marks it stopped, so the next
	// start of that session is known to be a change and is announced. It is for
	// the operator's stop of a pair whose session lives on; a pair whose session
	// is gone has nobody to announce to, and its history goes with it.
	KeepHistory bool
	// IncludeUnowned lets the stop act on units this state root recorded nothing
	// for. Only the operator at a terminal, by an explicit flag, sets it.
	IncludeUnowned bool
}

// ErrNotMine is what a stop of an unowned pair returns without IncludeUnowned.
var ErrNotMine = errors.New("keyholder: units for this pair are not recorded under this state root (another XDG_STATE_HOME?): not mine, left running")

// StopFound stops a pair found by its hash: under the same per-pair lock Start
// and Stop take, the holder, then fibby, are stopped and seen inactive before the
// directory is removed, and the key history is ended. Units of a pair whose
// directory is already gone are stopped by name all the same. A pair this state
// root did not record (Found.Unowned) is refused with ErrNotMine.
func (l *Launcher) StopFound(ctx context.Context, f Found) error {
	return l.StopFoundWith(ctx, f, StopOptions{})
}

// StopFoundWith is StopFound with options.
func (l *Launcher) StopFoundWith(ctx context.Context, f Found, opts StopOptions) error {
	if !hashRE.MatchString(f.Hash) {
		return fmt.Errorf("keyholder: %q is not a pair hash", f.Hash)
	}
	if f.Unowned && !opts.IncludeUnowned {
		return fmt.Errorf("%w (hash %s)", ErrNotMine, f.Hash)
	}
	name := f.Session
	if name == "" {
		name = "pair " + f.Hash
	}
	wait := lockWait
	if f.LockOnly {
		wait = 0 // a lock file with no pair: if anyone holds it, it is not ours to remove
	}
	h, err := l.acquire(ctx, f.Hash, name, wait)
	if errors.Is(err, errLockHeld) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := l.teardown(ctx, l.layoutForHash(f.Hash)); err != nil {
		h.Unlock()
		return err
	}
	hist := filepath.Join(l.Base, f.Hash+".last.json")
	if opts.KeepHistory {
		if err := l.markStopped(hist); err != nil {
			h.Unlock()
			return fmt.Errorf("marking the key history %s stopped: %w", hist, err)
		}
	} else if err := os.Remove(hist); err != nil && !errors.Is(err, os.ErrNotExist) {
		h.Unlock()
		return fmt.Errorf("removing the key history %s: %w", hist, err)
	} else if err := os.Remove(filepath.Join(l.Base, f.Hash+".kind")); err != nil && !errors.Is(err, os.ErrNotExist) {
		h.Unlock()
		return fmt.Errorf("removing the kind record of %s: %w", f.Hash, err)
	}
	h.Remove() // the lock file goes last, under the lock
	l.logf("keyholder: stopped the pair %s (%s)", f.Hash, strings.TrimSpace(f.Session))
	return nil
}

// markStopped records on a session's key history that its pair was stopped on
// purpose, keeping the last fingerprint. A pair with no history has nothing to
// mark.
func (l *Launcher) markStopped(path string) error {
	cur, ok := readLast(path)
	if !ok {
		return nil
	}
	now := time.Now()
	if l.Now != nil {
		now = l.Now()
	}
	cur.Stopped, cur.StoppedAt = true, now.UTC()
	return writeLast(path, cur)
}
