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
	now := time.Now()
	for _, e := range entries {
		if !e.IsDir() || !hashRE.MatchString(e.Name()) {
			continue
		}
		f := get(e.Name())
		f.Dir = filepath.Join(l.Base, e.Name())
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

// StopFound stops a pair found by its hash: under the same per-pair lock Start
// and Stop take, the holder, then fibby, are stopped and seen inactive before the
// directory is removed, and the key history is ended. Units of a pair whose
// directory is already gone are stopped by name all the same.
func (l *Launcher) StopFound(ctx context.Context, f Found) error {
	if !hashRE.MatchString(f.Hash) {
		return fmt.Errorf("keyholder: %q is not a pair hash", f.Hash)
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
	if err := os.Remove(hist); err != nil && !errors.Is(err, os.ErrNotExist) {
		h.Unlock()
		return fmt.Errorf("removing the key history %s: %w", hist, err)
	}
	h.Remove() // the lock file goes last, under the lock
	l.logf("keyholder: stopped the pair %s (%s)", f.Hash, strings.TrimSpace(f.Session))
	return nil
}
