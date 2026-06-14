// Package spawnhandshake is the spawn/fork readiness handshake (FDR 0006),
// carved out of the former internal/chat so spawn and fork no longer depend on
// the chat store — which moved to clown entirely (FDR 0017). A spawned worker
// signals readiness to its driver by writing a small file keyed by the
// (worker, driver) pair; the driver polls for it during launch.
//
// Deliberately stdlib-only — no chat, no clown, no internal/session import — so
// it is a leaf that both internal/hooks (the worker's SessionStart sender) and
// internal/spawn (the driver's waiter) can depend on without an import cycle.
package spawnhandshake

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// pollInterval is how often WaitForHello re-checks the handshake file between
// the immediate first check and the deadline.
const pollInterval = 250 * time.Millisecond

// hello is the on-disk handshake record: the worker's readiness timestamp,
// with the pair recorded for debuggability.
type hello struct {
	Worker string    `json:"worker"`
	Driver string    `json:"driver"`
	SentAt time.Time `json:"sent_at"`
}

// xdgStateBase returns $XDG_STATE_HOME or its fallback, so the handshake dir
// lands under the same root as the session index (and tests isolate it by
// pinning XDG_STATE_HOME). Mirrors the unexported helper in internal/session;
// inlined to keep this package a stdlib-only leaf.
func xdgStateBase() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state")
}

// dir is the handshake directory, a sibling of the session index.
func dir() string {
	return filepath.Join(xdgStateBase(), "spinclass", "spawn-handshake")
}

// pairPath is the handshake file for one (worker, driver) pair, keyed by a hash
// of the ordered pair: concurrent spawns of different workers never collide,
// and a re-spawn of the same pair reuses (overwrites) the slot.
func pairPath(worker, driver string) string {
	h := sha256.Sum256([]byte(worker + "\x00" + driver))
	return filepath.Join(dir(), fmt.Sprintf("%x.json", h[:16]))
}

// SendHello records the worker's readiness for its driver. Fired by the
// worker's SessionStart hook when its state carries spawned_by (FDR 0006). The
// write is atomic (temp file + rename) so the polling driver never reads a
// half-written record; a prior hello for the same pair is overwritten with a
// fresh timestamp.
func SendHello(worker, driver string) error {
	d := dir()
	if err := os.MkdirAll(d, 0o755); err != nil {
		return fmt.Errorf("create spawn-handshake dir: %w", err)
	}
	data, err := json.Marshal(hello{Worker: worker, Driver: driver, SentAt: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("marshal hello: %w", err)
	}
	final := pairPath(worker, driver)
	tmp, err := os.CreateTemp(d, ".tmp-*.json")
	if err != nil {
		return fmt.Errorf("create temp hello: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp hello: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp hello: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("commit hello: %w", err)
	}
	return nil
}

// WaitForHello blocks until the worker's hello (newer than since) appears for
// this driver, or deadline elapses (the error names the deadline and the worker
// key). Polls every pollInterval; the read is non-destructive, so a slow caller
// never loses the signal, and a stale hello from a prior spawn of the same pair
// is rejected by the since check.
func WaitForHello(driver, worker string, since time.Time, deadline time.Duration) error {
	arrived := func() (bool, error) {
		data, err := os.ReadFile(pairPath(worker, driver))
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		var h hello
		if err := json.Unmarshal(data, &h); err != nil {
			// A half-written or corrupt record: treat as not-yet-arrived
			// rather than failing the spawn; the next poll re-reads.
			return false, nil
		}
		return h.SentAt.After(since), nil
	}

	// Check once before the first tick so an already-arrived hello does not
	// pay the poll interval.
	if ok, err := arrived(); err != nil {
		return err
	} else if ok {
		return nil
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return fmt.Errorf("no hello from spawned session %s within %s (spawn handshake deadline, FDR 0006)", worker, deadline)
		case <-ticker.C:
			if ok, err := arrived(); err != nil {
				return err
			} else if ok {
				return nil
			}
		}
	}
}
