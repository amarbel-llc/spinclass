// Package servelog is a minimal file logger for `spinclass serve`.
//
// In serve mode os.Stdout carries the JSON-RPC framing, so diagnostics must
// go to a side-channel. This package opens a timestamped log file in
// $XDG_STATE_HOME/spinclass/logs/serve-<pid>.log (falling back to /tmp when
// the state dir can't be created) and exposes a tiny API for MCP handlers
// and startup code to write entry/exit and panic traces.
//
// All functions are safe to call before Open — they simply drop the write.
package servelog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

var (
	mu     sync.Mutex
	logger *log.Logger
	file   *os.File
	path   string
)

// Open initializes the log file. Safe to call more than once: subsequent
// calls without a matching Close are no-ops.
func Open() error {
	mu.Lock()
	defer mu.Unlock()

	if file != nil {
		return nil
	}

	dir := logDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		dir = os.TempDir()
	}

	name := fmt.Sprintf("serve-%d.log", os.Getpid())
	p := filepath.Join(dir, name)

	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("servelog: open %s: %w", p, err)
	}

	file = f
	path = p
	logger = log.New(f, "", log.LstdFlags|log.Lmicroseconds)
	logger.Printf("servelog.open pid=%d", os.Getpid())
	return nil
}

// Close flushes and closes the log file. Safe to call multiple times.
func Close() error {
	mu.Lock()
	defer mu.Unlock()

	if file == nil {
		return nil
	}
	logger.Printf("servelog.close pid=%d", os.Getpid())
	err := file.Close()
	file = nil
	logger = nil
	path = ""
	return err
}

// Path returns the path to the current log file, or "" if not open.
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	return path
}

// Infof writes an informational line. No-op if Open has not been called.
func Infof(format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if logger == nil {
		return
	}
	logger.Printf("INFO "+format, args...)
}

// Errorf writes an error line. No-op if Open has not been called.
func Errorf(format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if logger == nil {
		return
	}
	logger.Printf("ERROR "+format, args...)
}

func logDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "spinclass", "logs")
}
