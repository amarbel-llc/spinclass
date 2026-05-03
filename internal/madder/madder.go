// Package madder wraps the per-worktree madder blob-store init flow.
// The madder binary path is supplied at build time via internal/embeds
// + lib.mkSpinclass; when empty, every operation here is a no-op.
package madder

import (
	"fmt"
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
