// Package sessionlog is an append-only lifecycle log for session-state
// operations (Write, writeIndexSymlink, Remove, Tombstone, etc.).
//
// Opening the log file is best-effort: when $XDG_LOG_HOME and the
// fallback directory are both unwritable, every emit call silently no-ops
// rather than blocking spinclass. Logs are observability, not critical
// path — deleting the log file MUST NOT change spinclass behaviour
// (xdg_log_home(5)).
//
// The path is $XDG_LOG_HOME/spinclass/lifecycle.log, defaulting to
// $HOME/.local/log/spinclass/lifecycle.log per the XDG_LOG_HOME convention.
//
// Each emit captures runtime.Caller of the call site so a log line names
// the file:line that triggered the lifecycle event — the whole point of
// this package is making "who called session.Remove?" answerable after
// the fact.
package sessionlog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

var (
	mu     sync.Mutex
	logger *log.Logger
	logf   *os.File
	logp   string
)

// Open initializes the lifecycle log file. Safe to call more than once;
// subsequent calls without a matching Close are no-ops. Always returns
// nil — open failures degrade to silent no-op so callers can invoke this
// unconditionally at startup.
func Open() error {
	mu.Lock()
	defer mu.Unlock()

	if logf != nil {
		return nil
	}

	dir := logDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}

	p := filepath.Join(dir, "lifecycle.log")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}

	logf = f
	logp = p
	logger = log.New(f, "", log.LstdFlags|log.Lmicroseconds)
	logger.Printf("INFO sessionlog.go:0 sessionlog.open pid=%d", os.Getpid())
	return nil
}

// Close flushes and closes the log file. Safe to call multiple times.
func Close() error {
	mu.Lock()
	defer mu.Unlock()

	if logf == nil {
		return nil
	}
	err := logf.Close()
	logf = nil
	logger = nil
	logp = ""
	return err
}

// Path returns the path to the current log file, or "" if not open.
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	return logp
}

// Infof writes an informational line tagged with the caller's file:line.
// No-op when Open has not been called or failed silently.
func Infof(format string, args ...any) {
	logAt(2, "INFO", format, args...)
}

// Errorf writes an error line tagged with the caller's file:line.
// No-op when Open has not been called or failed silently.
func Errorf(format string, args ...any) {
	logAt(2, "ERROR", format, args...)
}

func logAt(skip int, level, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if logger == nil {
		return
	}
	_, src, line, ok := runtime.Caller(skip)
	caller := "?:0"
	if ok {
		caller = fmt.Sprintf("%s:%d", filepath.Base(src), line)
	}
	logger.Printf("%s %s %s", level, caller, fmt.Sprintf(format, args...))
}

func logDir() string {
	if base := os.Getenv("XDG_LOG_HOME"); base != "" {
		return filepath.Join(base, "spinclass")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "log", "spinclass")
}
