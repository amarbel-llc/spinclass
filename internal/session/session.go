package session

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	StateActive    = "active"
	StateInactive  = "inactive"
	StateAbandoned = "abandoned"
)

type State struct {
	PID          int               `json:"pid"`
	SessionState string            `json:"state"`
	RepoPath     string            `json:"repo_path"`
	WorktreePath string            `json:"worktree_path"`
	Branch       string            `json:"branch"`
	SessionKey   string            `json:"session_key"`
	Description  string            `json:"description,omitempty"`
	Entrypoint   []string          `json:"entrypoint"`
	Env          map[string]string `json:"env"`
	StartedAt    time.Time         `json:"started_at"`
	ExitedAt     *time.Time        `json:"exited_at,omitempty"`

	// isTombstone is set when the State was loaded from a regular file in
	// the central index (i.e. a session that was closed cleanly and whose
	// worktree-local state.json is gone). Unexported so it does not get
	// serialised. ResolveState honours it as StateAbandoned.
	isTombstone bool
}

// xdgStateBase returns $XDG_STATE_HOME or its fallback.
func xdgStateBase() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state")
}

// indexDir is the central directory of session-index entries. Each entry
// is keyed by sha256(worktree-abs-path)[:8] and is one of:
//   - a symlink resolving to <worktree>/.spinclass/state.json (live)
//   - a regular file (tombstone — final state preserved after clean close)
//   - a dangling symlink (externally closed, e.g. git worktree remove)
func indexDir() string {
	return filepath.Join(xdgStateBase(), "spinclass", "index")
}

// indexFilename hashes the worktree absolute path. Slice 1 keeps the same
// 8-byte truncation used by the legacy stateFilename for visual continuity.
func indexFilename(worktreeAbsPath string) string {
	h := sha256.Sum256([]byte(filepath.Clean(worktreeAbsPath)))
	return fmt.Sprintf("%x.json", h[:8])
}

// indexPath returns the full path of an index entry for the given worktree.
func indexPath(worktreeAbsPath string) string {
	return filepath.Join(indexDir(), indexFilename(worktreeAbsPath))
}

// worktreeStatePath returns the worktree-local state file path.
func worktreeStatePath(worktreeAbsPath string) string {
	return filepath.Join(worktreeAbsPath, ".spinclass", "state.json")
}

// worktreeFromRepoBranch reconstructs the conventional worktree path
// `<repo>/.worktrees/<branch>`. Read/Write/Remove accept (repoPath, branch)
// for backwards-compatibility with existing callers.
func worktreeFromRepoBranch(repoPath, branch string) string {
	return filepath.Join(repoPath, ".worktrees", branch)
}

// legacyStateDir / legacyStateFilename / legacyStatePath describe the
// pre-slice-1 layout under $XDG_STATE_HOME/spinclass/sessions/. Retained
// for the one-shot migration in migrateOnce.
func legacyStateDir() string {
	return filepath.Join(xdgStateBase(), "spinclass", "sessions")
}

func legacyStateFilename(repoPath, branch string) string {
	h := sha256.Sum256([]byte(repoPath + "/" + branch))
	return fmt.Sprintf("%x-state.json", h[:8])
}

func legacyStatePath(repoPath, branch string) string {
	return filepath.Join(legacyStateDir(), legacyStateFilename(repoPath, branch))
}

// Write persists a session state. The worktree must exist on disk; the
// `.spinclass/` directory inside it is created on demand. The central
// index entry is written atomically as a symlink pointing at the
// worktree-local file, replacing whatever was previously at that path
// (including a stale tombstone if the session is reactivating).
func Write(s State) error {
	migrateOnce()

	wt := s.WorktreePath
	if wt == "" {
		return errors.New("session.Write: WorktreePath required")
	}
	if _, err := os.Stat(wt); err != nil {
		return fmt.Errorf("session.Write: worktree %q: %w", wt, err)
	}

	dir := filepath.Join(wt, ".spinclass")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	statePath := worktreeStatePath(wt)
	if err := os.WriteFile(statePath, data, 0o644); err != nil {
		return err
	}

	if err := os.MkdirAll(indexDir(), 0o755); err != nil {
		return err
	}
	if err := writeIndexSymlink(wt); err != nil {
		return err
	}
	return nil
}

// writeIndexSymlink atomically (re)points the central index entry for
// worktree at the worktree-local state.json. Existing entries — symlink
// or tombstone — are replaced.
func writeIndexSymlink(worktreeAbsPath string) error {
	target := worktreeStatePath(worktreeAbsPath)
	link := indexPath(worktreeAbsPath)

	tmp, err := os.CreateTemp(indexDir(), ".tmp-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	tmp.Close()
	if err := os.Remove(tmpName); err != nil {
		return err
	}
	if err := os.Symlink(target, tmpName); err != nil {
		return err
	}
	if err := os.Rename(tmpName, link); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Read returns the State for (repoPath, branch). Live sessions resolve via
// the worktree-local state.json. If that file is missing but a tombstone
// exists in the central index, Read returns the tombstone with isTombstone
// set. Returns os.ErrNotExist (or a wrapping thereof) when neither source
// has data.
func Read(repoPath, branch string) (*State, error) {
	migrateOnce()

	wt := worktreeFromRepoBranch(repoPath, branch)
	if data, err := os.ReadFile(worktreeStatePath(wt)); err == nil {
		var s State
		if jerr := json.Unmarshal(data, &s); jerr != nil {
			return nil, jerr
		}
		return &s, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// Fall back to a tombstone at the index path. This lets tooling read
	// final state of cleanly-closed sessions via the same API.
	idx := indexPath(wt)
	info, err := os.Lstat(idx)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Symlink — would have resolved above if live. Treat as missing.
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(idx)
	if err != nil {
		return nil, err
	}
	var s State
	if jerr := json.Unmarshal(data, &s); jerr != nil {
		return nil, jerr
	}
	s.isTombstone = true
	return &s, nil
}

// Remove deletes both the worktree-local state file and the central index
// entry. Tolerates missing files. Used by callers that have torn down the
// worktree (sc close, sc clean) and by abandoned-session reaping. To
// preserve close history, callers should call Tombstone instead before
// removing the worktree.
func Remove(repoPath, branch string) error {
	migrateOnce()
	wt := worktreeFromRepoBranch(repoPath, branch)
	return removeForWorktree(wt)
}

func removeForWorktree(worktreeAbsPath string) error {
	statePath := worktreeStatePath(worktreeAbsPath)
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Best-effort RemoveAll the .spinclass dir so it doesn't outlive the
	// state file. Ignore "not exist" and "not empty" — slice 2's lifecycle
	// hooks may write siblings here.
	_ = os.Remove(filepath.Dir(statePath))

	idx := indexPath(worktreeAbsPath)
	if err := os.Remove(idx); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Tombstone marks the session as cleanly closed: reads the live state,
// atomically replaces the central index symlink with a regular file
// containing the same JSON (the tombstone), and removes the worktree-local
// .spinclass directory. Callers must invoke Tombstone BEFORE deleting the
// worktree directory itself, since the read needs <worktree>/.spinclass/
// state.json to still be present.
//
// Defined in slice 1 as plumbing but not yet wired up to merge/close/clean
// — the lifecycle work in #42 picks this up.
func Tombstone(repoPath, branch string) error {
	migrateOnce()
	wt := worktreeFromRepoBranch(repoPath, branch)
	statePath := worktreeStatePath(wt)
	data, err := os.ReadFile(statePath)
	if err != nil {
		return fmt.Errorf("session.Tombstone: read live state: %w", err)
	}

	if err := os.MkdirAll(indexDir(), 0o755); err != nil {
		return err
	}
	idx := indexPath(wt)
	tmp, err := os.CreateTemp(indexDir(), ".tomb-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, werr := tmp.Write(data); werr != nil {
		tmp.Close()
		os.Remove(tmpName)
		return werr
	}
	if cerr := tmp.Close(); cerr != nil {
		os.Remove(tmpName)
		return cerr
	}
	if err := os.Rename(tmpName, idx); err != nil {
		os.Remove(tmpName)
		return err
	}
	// State file and the .spinclass dir are now redundant — clean up.
	_ = os.Remove(statePath)
	_ = os.Remove(filepath.Dir(statePath))
	return nil
}

func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

// ResolveState checks the actual state, handling crash recovery.
// If the session was loaded from a tombstone, returns StateAbandoned.
// If the worktree dir doesn't exist, returns StateAbandoned.
// If state file says "active" but PID is dead, returns StateInactive.
func (s *State) ResolveState() string {
	if s.isTombstone {
		return StateAbandoned
	}
	if _, err := os.Stat(s.WorktreePath); os.IsNotExist(err) {
		return StateAbandoned
	}
	if s.SessionState == StateActive && !IsAlive(s.PID) {
		return StateInactive
	}
	return s.SessionState
}

// FindByWorktreePath returns the session whose WorktreePath is path or
// contains it. Symlinks on either side are resolved before comparison so
// a symlink-backed cwd matches a real worktree, and component-aware
// matching prevents `/foo/bar` from matching the unrelated `/foo/bar-baz`.
//
// Slice 1 still scans the index because the input path may be inside a
// worktree subdirectory; we don't know the worktree root a priori. We do
// short-circuit if a direct lookup at the resolved path's index entry
// already matches, which is the common case.
func FindByWorktreePath(path string) (*State, error) {
	migrateOnce()
	resolvedPath := evalOrClean(path)

	// Direct lookup: try the resolved path itself as a worktree root.
	if direct, ok := readDirectIfMatches(resolvedPath); ok {
		return direct, nil
	}

	states, err := ListAll()
	if err != nil {
		return nil, err
	}
	for i := range states {
		s := &states[i]
		if pathInsideResolved(resolvedPath, evalOrClean(s.WorktreePath)) {
			return s, nil
		}
	}
	return nil, fmt.Errorf("no session found for path %s", path)
}

// readDirectIfMatches attempts to load the index entry whose key derives
// from worktreeAbsPath, returning (state, true) only if the entry exists
// and resolves successfully (live or tombstone).
func readDirectIfMatches(worktreeAbsPath string) (*State, bool) {
	idx := indexPath(worktreeAbsPath)
	info, err := os.Lstat(idx)
	if err != nil {
		return nil, false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Live symlink — try to read through it.
		data, rerr := os.ReadFile(idx)
		if rerr != nil {
			return nil, false
		}
		var s State
		if jerr := json.Unmarshal(data, &s); jerr != nil {
			return nil, false
		}
		return &s, true
	}
	// Regular file: tombstone.
	data, rerr := os.ReadFile(idx)
	if rerr != nil {
		return nil, false
	}
	var s State
	if jerr := json.Unmarshal(data, &s); jerr != nil {
		return nil, false
	}
	s.isTombstone = true
	return &s, true
}

// pathInsideResolved reports whether path is exactly root or sits
// beneath it as a path-component prefix. Both arguments must already be
// canonicalised by evalOrClean so symlinks compare correctly and
// `/foo/bar` does not match `/foo/bar-baz`.
func pathInsideResolved(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

// evalOrClean resolves symlinks where possible, falling back to lexical
// Clean for paths that no longer exist (e.g. a worktree that was just
// removed but whose state file is still on disk).
func evalOrClean(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// FindByID scans all session state entries and returns the one whose
// WorktreePath ends in /.worktrees/<id>. The id is the worktree directory
// name, which may differ from the git branch.
func FindByID(id string) (*State, error) {
	migrateOnce()
	suffix := "/.worktrees/" + id
	states, err := ListAll()
	if err != nil {
		return nil, err
	}
	for i := range states {
		s := &states[i]
		if strings.HasSuffix(s.WorktreePath, suffix) {
			return s, nil
		}
	}
	return nil, fmt.Errorf("no session found for worktree ID %q", id)
}

// SortStates orders sessions in place so active sessions come first
// and otherwise sorts alphabetically by branch. Both completer output
// and the interactive picker share this so callers get the same
// ordering everywhere.
func SortStates(states []State) {
	sort.SliceStable(states, func(i, j int) bool {
		ai := states[i].ResolveState() == StateActive
		aj := states[j].ResolveState() == StateActive
		if ai != aj {
			return ai
		}
		return states[i].Branch < states[j].Branch
	})
}

// ListForRepo returns sessions whose RepoPath matches and whose resolved
// state is not abandoned.
func ListForRepo(repoPath string) ([]State, error) {
	all, err := ListAll()
	if err != nil {
		return nil, err
	}
	var filtered []State
	for i := range all {
		s := &all[i]
		if s.RepoPath == repoPath && s.ResolveState() != StateAbandoned {
			filtered = append(filtered, *s)
		}
	}
	return filtered, nil
}

// ListAll walks the central index and returns every session it can read.
// Live entries (symlinks that resolve), tombstones (regular files), and
// dangling symlinks all appear; ResolveState honours each appropriately.
func ListAll() ([]State, error) {
	migrateOnce()

	dir := indexDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var states []State
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		full := filepath.Join(dir, e.Name())

		info, lerr := os.Lstat(full)
		if lerr != nil {
			continue
		}

		isSymlink := info.Mode()&os.ModeSymlink != 0
		isTomb := !isSymlink

		// Read through the entry. For dangling symlinks, ReadFile errors
		// — we still want to surface those entries (callers may want to
		// reap them). Synthesise a minimal abandoned State from the
		// (now-broken) target path.
		data, rerr := os.ReadFile(full)
		if rerr != nil {
			if isSymlink {
				if target, terr := os.Readlink(full); terr == nil {
					states = append(states, danglingStateFromTarget(target))
				}
			}
			continue
		}
		var s State
		if jerr := json.Unmarshal(data, &s); jerr != nil {
			continue
		}
		s.isTombstone = isTomb
		states = append(states, s)
	}
	return states, nil
}

// danglingStateFromTarget builds a synthetic State for a dangling-symlink
// index entry. We can recover WorktreePath from the link target (which
// was `<worktree>/.spinclass/state.json`) by walking up two levels, plus
// the branch from the directory name. Other fields are blank — callers
// that want richer info on dangling entries should reap them.
func danglingStateFromTarget(target string) State {
	worktree := filepath.Dir(filepath.Dir(target))
	branch := filepath.Base(worktree)
	s := State{
		WorktreePath: worktree,
		Branch:       branch,
		SessionState: StateAbandoned,
	}
	// RepoPath is everything above /.worktrees/<branch>.
	if parent := filepath.Dir(filepath.Dir(worktree)); filepath.Base(filepath.Dir(worktree)) == ".worktrees" {
		s.RepoPath = parent
		s.SessionKey = filepath.Base(parent) + "/" + branch
	}
	return s
}

// ============================================================================
// Migration: one-shot, idempotent move from
//   $XDG_STATE_HOME/spinclass/sessions/<hash>-state.json
// to the new layout (<worktree>/.spinclass/state.json + central symlink).
// Triggered via sync.Once at the top of every public storage entry point.
// ============================================================================

var migrateGate sync.Once

func migrateOnce() {
	migrateGate.Do(func() {
		_ = runMigration()
	})
}

// MigrateNow forces a migration pass, ignoring the once gate. Tests use
// it to reset state between invocations; production code shouldn't need it.
func MigrateNow() error {
	return runMigration()
}

func runMigration() error {
	oldDir := legacyStateDir()
	entries, err := os.ReadDir(oldDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	var residual int
	for _, e := range entries {
		if e.IsDir() {
			residual++
			continue
		}
		oldFile := filepath.Join(oldDir, e.Name())
		data, rerr := os.ReadFile(oldFile)
		if rerr != nil {
			residual++
			continue
		}
		var s State
		if jerr := json.Unmarshal(data, &s); jerr != nil {
			residual++
			continue
		}

		// If the worktree no longer exists, the session is abandoned —
		// drop the stale state file (the new layout has no place to put
		// it; slice 3's tombstone retention handles closed history).
		if _, werr := os.Stat(s.WorktreePath); errors.Is(werr, os.ErrNotExist) {
			os.Remove(oldFile)
			continue
		}

		// Write into the new layout. We bypass migrateOnce here (we're
		// inside it) and call the storage primitives' inner steps
		// directly to avoid recursion.
		if merr := migrateOne(s); merr != nil {
			residual++
			continue
		}
		if err := os.Remove(oldFile); err != nil {
			residual++
			continue
		}
	}

	if residual == 0 {
		_ = os.Remove(oldDir)
	}
	return nil
}

// migrateOne mirrors Write's storage steps without re-entering migrateOnce.
func migrateOne(s State) error {
	wt := s.WorktreePath
	dir := filepath.Join(wt, ".spinclass")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(worktreeStatePath(wt), data, 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(indexDir(), 0o755); err != nil {
		return err
	}
	return writeIndexSymlink(wt)
}
