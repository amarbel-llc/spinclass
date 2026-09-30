// Package madder wraps the per-worktree madder blob-store init flow.
// The madder binary path is supplied at build time via internal/embeds
// + lib.mkSpinclass; when empty, every operation here is a no-op.
package madder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Public contract knobs. ExcludePattern lands in `.git/info/exclude`
// and AllowRule lands in Claude Code's permission allow-list when
// madder activation runs.
const (
	ExcludePattern = ".madder/"
	AllowRule      = "Bash(madder:*)"
)

// blobStoreConfigRel is the marker file madder creates on a successful
// init. `madder init` is not idempotent (it fails on a second run,
// per madder's init_idempotent_fails test), so spinclass checks for
// this file before invoking init.
const blobStoreConfigRel = ".madder/local/share/blob_stores/default/blob_store-config"

// StoreReady reports whether the per-worktree blob store at
// worktreePath has already been initialised.
func StoreReady(worktreePath string) bool {
	_, err := os.Stat(filepath.Join(worktreePath, blobStoreConfigRel))
	return err == nil
}

// LinkInto atomically (re)points `<binDir>/madder` at binPath so the
// build-time-pinned binary is reachable via PATH inside session shells
// and tools that can't see the burned-in absolute path. Callers wire
// binDir to a directory already on the session PATH (e.g.
// `<git-common-dir>/spinclass/bin/`).
//
// No-op when binPath is empty. Uses tempfile+rename so concurrent
// invocations don't race on a partial-state path.
func LinkInto(binDir, binPath string) error {
	if binPath == "" {
		return nil
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("creating shim dir: %w", err)
	}

	// Pid+nano gives a unique-by-construction temp name with no
	// intermediary regular file; we go straight to Symlink. Avoids
	// the create-file/Remove/Symlink TOCTOU window that the simpler
	// CreateTemp pattern would expose.
	link := filepath.Join(binDir, "madder")
	tmpName := filepath.Join(binDir, fmt.Sprintf(".tmp-madder-%d-%d", os.Getpid(), time.Now().UnixNano()))
	if err := os.Symlink(binPath, tmpName); err != nil {
		return fmt.Errorf("creating temp symlink: %w", err)
	}
	if err := os.Rename(tmpName, link); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming temp to %s: %w", link, err)
	}
	return nil
}

// Write spawns `madder write -format json .default -` against the
// per-worktree store and returns a writer piped into its stdin plus a
// finish() callback that waits on the subprocess and returns the
// resulting blob id. Callers tee bytes into the writer (alongside an
// in-memory tail ring), close it when the producer is done, and call
// finish().
//
// The returned writer never returns an error: madder decodes every
// store config under the tree before honouring a store id, so a sibling
// config the pinned madder can't decode makes it exit before reading. A
// sink error must never propagate into the hook's pipe (#349); the
// first write error is latched and reported by finish() instead.
//
// No-op when binPath is empty: writer wraps io.Discard, finish()
// returns ("", nil), no process is spawned.
//
// MADDER_CEILING_DIRECTORIES is scoped to this invocation only.
func Write(worktreePath, binPath string) (io.WriteCloser, func() (string, error), error) {
	if binPath == "" {
		return discardWriteCloser{}, func() (string, error) { return "", nil }, nil
	}

	cmd := exec.Command(binPath, "write", "-format", "json", ".default", "-")
	cmd.Dir = worktreePath
	cmd.Env = append(os.Environ(), "MADDER_CEILING_DIRECTORIES="+worktreePath)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("madder write: stdin pipe: %w", err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, nil, fmt.Errorf("madder write: start: %w", err)
	}

	sink := &latchingWriter{w: stdin}

	finish := func() (string, error) {
		// Caller is expected to Close stdin to signal EOF; defensively
		// close again here so finish() is safe to call without it.
		_ = sink.Close()
		if err := cmd.Wait(); err != nil {
			msg := bytes.TrimSpace(stderr.Bytes())
			return "", fmt.Errorf("madder write: %w\n%s", err, msg)
		}
		if err := sink.latched(); err != nil {
			return "", fmt.Errorf("madder write: stdin closed before all bytes were written: %w", err)
		}
		var resp struct {
			ID     string `json:"id"`
			Size   int64  `json:"size"`
			Source string `json:"source"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
			return "", fmt.Errorf("madder write: parsing response %q: %w", stdout.String(), err)
		}
		return resp.ID, nil
	}
	return sink, finish, nil
}

// latchingWriter forwards to w until the first error, latches it, and
// swallows every later byte without a syscall. Write always reports
// success so an io.MultiWriter tee is never short-circuited.
type latchingWriter struct {
	mu  sync.Mutex
	w   io.WriteCloser
	err error
}

func (l *latchingWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err == nil {
		if _, err := l.w.Write(p); err != nil {
			l.err = err
		}
	}
	return len(p), nil
}

func (l *latchingWriter) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Close()
}

func (l *latchingWriter) latched() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

// Init initialises the per-worktree blob store at worktreePath using
// binPath as the madder binary. Skipped if binPath is empty or the
// store is already ready. The invocation matches madder's bats suite
// (`madder init -encryption none .default`).
//
// MADDER_CEILING_DIRECTORIES is scoped to the init invocation so
// madder cannot walk up into a parent repo's .madder/ during store
// discovery; exporting it into the session would be too broad.
func Init(worktreePath, binPath string) error {
	if binPath == "" {
		return nil
	}
	if StoreReady(worktreePath) {
		return nil
	}

	cmd := exec.Command(binPath, "init", "-encryption", "none", ".default")
	cmd.Dir = worktreePath
	cmd.Env = append(os.Environ(), "MADDER_CEILING_DIRECTORIES="+worktreePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("madder init: %w\n%s", err, out)
	}
	return nil
}
