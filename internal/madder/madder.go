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
		os.Remove(tmpName)
		return fmt.Errorf("renaming temp to %s: %w", link, err)
	}
	return nil
}

// Write spawns `madder write -format json -` against the per-worktree
// store and returns a writer piped into its stdin plus a finish()
// callback that waits on the subprocess and returns the resulting
// blob id. Callers tee bytes into the writer (alongside an in-memory
// tail ring), close it when the producer is done, and call finish().
//
// No-op when binPath is empty: writer wraps io.Discard, finish()
// returns ("", nil), no process is spawned.
//
// MADDER_CEILING_DIRECTORIES is scoped to this invocation only.
func Write(worktreePath, binPath string) (io.WriteCloser, func() (string, error), error) {
	if binPath == "" {
		return discardWriteCloser{}, func() (string, error) { return "", nil }, nil
	}

	cmd := exec.Command(binPath, "write", "-format", "json", "-")
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
		stdin.Close()
		return nil, nil, fmt.Errorf("madder write: start: %w", err)
	}

	finish := func() (string, error) {
		// Caller is expected to Close stdin to signal EOF; defensively
		// close again here so finish() is safe to call without it.
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			msg := bytes.TrimSpace(stderr.Bytes())
			return "", fmt.Errorf("madder write: %w\n%s", err, msg)
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
	return stdin, finish, nil
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
