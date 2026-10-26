// Package spawnhandshake is the spawn/fork readiness handshake (FDR 0006),
// carved out of the former internal/chat so spawn and fork no longer depend on
// the chat store — which moved to clown entirely (FDR 0017). A spawned worker
// signals readiness to its driver by writing a small file keyed by the
// (worker, driver) pair; the driver polls for it during launch.
//
// Deliberately stdlib-only — no chat, no clown, no internal/alfa/session import — so
// it is a leaf that both internal/delta/hooks (the worker's SessionStart sender) and
// internal/foxtrot/spawn (the driver's waiter) can depend on without an import cycle.
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
// with the pair recorded for debuggability. PoshSessionID carries the worker's
// multiplexer session name (clown's minted UUID == the claude --session-id)
// back to the driver so it can reattach with `posh attach <id>` — the id is
// crypto-random per spawn and unknowable until the worker boots, which is why
// the worker reports it here (FDR-0017 reattach; direction B).
type hello struct {
	Worker        string    `json:"worker"`
	Driver        string    `json:"driver"`
	SentAt        time.Time `json:"sent_at"`
	PoshSessionID string    `json:"posh_session_id,omitempty"`
}

// xdgStateBase returns $XDG_STATE_HOME or its fallback, so the handshake dir
// lands under the same root as the session index (and tests isolate it by
// pinning XDG_STATE_HOME). Mirrors the unexported helper in internal/alfa/session;
// inlined to keep this package a stdlib-only leaf.
func xdgStateBase() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state")
}

// DirEnv names the environment variable that points a worker at its own
// handshake directory. A confined worker's state dir is not the frontend's
// (the unit has its own home and a read-only view of the host's), so the hello
// it wrote to $XDG_STATE_HOME/spinclass/spawn-handshake could neither be
// created nor seen by the driver. The frontend instead binds one directory per
// session read-write into the unit and names it here; the driver reads the same
// path. Unset (every unconfined spawn) means the shared directory below.
const DirEnv = "SPINCLASS_HANDSHAKE_DIR"

// SharedDir is the shared handshake directory, a sibling of the session index.
func SharedDir() string {
	return filepath.Join(xdgStateBase(), "spinclass", "spawn-handshake")
}

// SessionDir is the per-session handshake directory a confined worker gets,
// keyed by its worktree path. It is a subdirectory of the shared one so a unit
// that has it bound read-write can write only its own hello, never a sibling's.
func SessionDir(stateBase, worktree string) string {
	h := sha256.Sum256([]byte(filepath.Clean(worktree)))
	return filepath.Join(stateBase, "spinclass", "spawn-handshake", "session-"+fmt.Sprintf("%x", h[:8]))
}

// dir is the directory a hello is written to: the one the environment names,
// else the shared one.
func dir() string {
	if d := os.Getenv(DirEnv); d != "" {
		return d
	}
	return SharedDir()
}

// pairPath is the handshake file for one (worker, driver) pair in d, keyed by a
// hash of the ordered pair: concurrent spawns of different workers never
// collide, and a re-spawn of the same pair reuses (overwrites) the slot.
func pairPath(d, worker, driver string) string {
	h := sha256.Sum256([]byte(worker + "\x00" + driver))
	return filepath.Join(d, fmt.Sprintf("%x.json", h[:16]))
}

// SendHello records the worker's readiness for its driver. Fired by the
// worker's SessionStart hook when its state carries spawned_by (FDR 0006). The
// write is atomic (temp file + rename) so the polling driver never reads a
// half-written record; a prior hello for the same pair is overwritten with a
// fresh timestamp. poshSessionID is the worker's multiplexer session name for
// the driver's reattach; empty is fine (older/unknown workers) — the driver
// just skips the auto-attach.
func SendHello(worker, driver, poshSessionID string) error {
	d := dir()
	if err := os.MkdirAll(d, 0o755); err != nil {
		return fmt.Errorf("create spawn-handshake dir: %w", err)
	}
	data, err := json.Marshal(hello{Worker: worker, Driver: driver, SentAt: time.Now().UTC(), PoshSessionID: poshSessionID})
	if err != nil {
		return fmt.Errorf("marshal hello: %w", err)
	}
	final := pairPath(d, worker, driver)
	tmp, err := os.CreateTemp(d, ".tmp-*.json")
	if err != nil {
		return fmt.Errorf("create temp hello: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp hello: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp hello: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("commit hello: %w", err)
	}
	return nil
}

// WaitForHello blocks until the worker's hello (newer than since) appears for
// this driver, or deadline elapses (the error names the deadline and the worker
// key). Polls every pollInterval; the read is non-destructive, so a slow caller
// never loses the signal, and a stale hello from a prior spawn of the same pair
// is rejected by the since check. On success it returns the worker's reported
// posh session id (empty when the worker did not report one).
func WaitForHello(driver, worker string, since time.Time, deadline time.Duration) (string, error) {
	return WaitForHelloIn(SharedDir(), driver, worker, since, deadline)
}

// WaitForHelloIn is WaitForHello reading the hello from handshake directory d:
// a confined worker's per-session directory (SessionDir) rather than the
// shared one.
func WaitForHelloIn(d, driver, worker string, since time.Time, deadline time.Duration) (string, error) {
	arrived := func() (bool, string, error) {
		data, err := os.ReadFile(pairPath(d, worker, driver))
		if err != nil {
			if os.IsNotExist(err) {
				return false, "", nil
			}
			return false, "", err
		}
		var h hello
		if err := json.Unmarshal(data, &h); err != nil {
			// A half-written or corrupt record: treat as not-yet-arrived
			// rather than failing the spawn; the next poll re-reads.
			return false, "", nil
		}
		if !h.SentAt.After(since) {
			return false, "", nil
		}
		return true, h.PoshSessionID, nil
	}

	// Check once before the first tick so an already-arrived hello does not
	// pay the poll interval.
	if ok, id, err := arrived(); err != nil {
		return "", err
	} else if ok {
		return id, nil
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return "", fmt.Errorf("no hello from spawned session %s within %s (spawn handshake deadline, FDR 0006)", worker, deadline)
		case <-ticker.C:
			if ok, id, err := arrived(); err != nil {
				return "", err
			} else if ok {
				return id, nil
			}
		}
	}
}
