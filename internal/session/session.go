package session

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/amarbel-llc/spinclass/internal/sessionlog"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

// caller returns "file.go:N" of the call site `skip` frames up from
// the caller of caller(). caller(1) inside session.Write returns the
// external site that invoked session.Write. Used to tag lifecycle log
// entries so "who called session.Remove?" can be answered after the fact.
func caller(skip int) string {
	_, file, line, ok := runtime.Caller(skip + 1)
	if !ok {
		return "?:0"
	}
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}

// debugLogger normalises a possibly-nil *slog.Logger into one that
// silently discards all records. Callers that want exclusion diagnostics
// pass a real logger; everyone else passes nil.
func debugLogger(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.New(slog.DiscardHandler)
}

const (
	StateActive    = "active"
	StateInactive  = "inactive"
	StateAbandoned = "abandoned"
	// StateRunningDetached means the spinclass-spawned entrypoint has
	// exited, but a configured liveness probe reported the underlying
	// multiplexer group (or equivalent) is still attachable. Set by
	// shop.Attach after cmd.Wait returns when the probe exits 0.
	StateRunningDetached = "running-detached"
)

// KindImplicit marks a session materialized for a repo's main checkout (no
// sc-created worktree). Absent Kind ⇒ a normal worktree session.
const KindImplicit = "implicit"

type State struct {
	PID          int    `json:"pid"`
	SessionState string `json:"state"`
	RepoPath     string `json:"repo_path"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	SessionKey   string `json:"session_key"`
	Kind         string `json:"kind,omitempty"`
	Description  string `json:"description,omitempty"`
	// SpawnedBy is the driver session's key (<repo>/<branch>) recorded by
	// `sc spawn` / detached fork when this session was launched as a worker.
	// Display-only lineage surfaced by `sc list` and chat-list-sessions —
	// no behavioral branches key off it. See FDR 0006.
	SpawnedBy string `json:"spawned_by,omitempty"`
	// HelloSentAt records when the SessionStart hook emitted the spawn
	// hello to SpawnedBy, deduping re-fires (resume/clear/compact). Set
	// only on spawned sessions (FDR 0006).
	HelloSentAt *time.Time        `json:"hello_sent_at,omitempty"`
	Entrypoint  []string          `json:"entrypoint"`
	Env         map[string]string `json:"env"`
	StartedAt   time.Time         `json:"started_at"`
	ExitedAt    *time.Time        `json:"exited_at,omitempty"`

	// PreMergeAttestation buffers the agent's most recent
	// nothing-but-the-truth response. Consumed and cleared by the next
	// merge-this-session / check-this-session call. See
	// docs/features/0007-pre-merge-skill-attestation.md.
	PreMergeAttestation *PreMergeAttestation `json:"pre_merge_attestation,omitempty"`

	// SetupFingerprint / SetupScheme record the setupfingerprint.Compute
	// hash (and its scheme version) captured the last time this worktree's
	// setup was applied (`sc start`, `sc rebuild`, or resume auto-rebuild).
	// A mismatch against the freshly-computed fingerprint flags the worktree
	// as stale (setup drifted from current config/binary/pins) — see the
	// rebuild design. Empty/zero on worktrees that predate staleness tracking,
	// which reads as stale (a one-time rebuild). SetupAt is the wall-clock of
	// that last apply, for display.
	SetupFingerprint string     `json:"setup_fingerprint,omitempty"`
	SetupScheme      int        `json:"setup_scheme,omitempty"`
	SetupAt          *time.Time `json:"setup_at,omitempty"`

	// isTombstone is set when the State was loaded from a regular file in
	// the central index (i.e. a session that was closed cleanly and whose
	// worktree-local state.json is gone). Unexported so it does not get
	// serialised. ResolveState honours it as StateAbandoned.
	isTombstone bool
}

// PreMergeAttestation records one nothing-but-the-truth call. Lifetime
// is single-use: the next gated MCP tool consumes the field and clears
// it via session.Write.
type PreMergeAttestation struct {
	RecordedAt time.Time       `json:"recorded_at"`
	Skills     []AttestedSkill `json:"skills"`
}

// AttestedSkill is one entry from the agent's response.
type AttestedSkill struct {
	Name      string `json:"name"`
	Used      bool   `json:"used"`
	Reasoning string `json:"reasoning"`
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

	from := caller(1)
	wt := s.WorktreePath
	if wt == "" {
		sessionlog.Errorf("session.Write rejected (empty WorktreePath) from=%s", from)
		return errors.New("session.Write: WorktreePath required")
	}
	if _, err := os.Stat(wt); err != nil {
		sessionlog.Errorf("session.Write worktree-stat-failed wt=%s branch=%s from=%s err=%v", wt, s.Branch, from, err)
		return fmt.Errorf("session.Write: worktree %q: %w", wt, err)
	}

	dir := filepath.Join(wt, ".spinclass")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		sessionlog.Errorf("session.Write mkdir-failed dir=%s from=%s err=%v", dir, from, err)
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	statePath := worktreeStatePath(wt)
	if err := os.WriteFile(statePath, data, 0o644); err != nil {
		sessionlog.Errorf("session.Write writefile-failed path=%s from=%s err=%v", statePath, from, err)
		return err
	}

	if err := os.MkdirAll(indexDir(), 0o755); err != nil {
		return err
	}
	if err := writeIndexSymlink(wt); err != nil {
		sessionlog.Errorf("session.Write symlink-failed wt=%s branch=%s from=%s err=%v", wt, s.Branch, from, err)
		return err
	}
	sessionlog.Infof("session.Write wt=%s branch=%s state=%s pid=%d from=%s", wt, s.Branch, s.SessionState, s.PID, from)
	return nil
}

// writeIndexSymlink atomically (re)points the central index entry for
// worktree at the worktree-local state.json. Existing entries — symlink
// or tombstone — are replaced.
//
// Pid+nano gives a unique-by-construction temp name with no
// intermediary regular file; we go straight to Symlink. Avoids the
// create-file/Remove/Symlink TOCTOU window that the simpler
// CreateTemp pattern would expose.
func writeIndexSymlink(worktreeAbsPath string) error {
	target := worktreeStatePath(worktreeAbsPath)
	link := indexPath(worktreeAbsPath)

	tmpName := filepath.Join(indexDir(), fmt.Sprintf(".tmp-%d-%d.json", os.Getpid(), time.Now().UnixNano()))
	if err := os.Symlink(target, tmpName); err != nil {
		return err
	}
	if err := os.Rename(tmpName, link); err != nil {
		_ = os.Remove(tmpName)
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

// EnsureWorktreeState returns the State for (repoPath, branch) if one exists,
// or synthesizes a minimal active State when none does — i.e. the worktree
// was never attached via `sc start`/`resume` (an agent ran the harness in it
// directly) or its state was removed. Single-field updaters (notably
// update-this-session-description) use this to auto-heal an untracked
// worktree session rather than surfacing the raw "missing index" lstat (#161),
// mirroring the implicit-session lazy-materialization precedent (#141).
//
// The synthesized State uses the conventional <repo>/.worktrees/<branch>
// worktree path so a later Read round-trips, the caller-supplied sessionKey
// (typically $SPINCLASS_SESSION_ID, else <repo-dirname>/<branch>), and pid as
// the liveness PID (the serve process, which lives as long as the session).
// It is NOT persisted: the caller mutates the field it owns and calls Write,
// keeping one persist point. A non-not-exist Read error is propagated.
func EnsureWorktreeState(repoPath, branch, sessionKey string, pid int) (*State, error) {
	st, err := Read(repoPath, branch)
	if err == nil {
		return st, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return &State{
		PID:          pid,
		SessionState: StateActive,
		RepoPath:     repoPath,
		WorktreePath: worktreeFromRepoBranch(repoPath, branch),
		Branch:       branch,
		SessionKey:   sessionKey,
		StartedAt:    time.Now().UTC(),
		Env:          map[string]string{"SPINCLASS_SESSION_ID": sessionKey},
	}, nil
}

// Remove deletes both the worktree-local state file and the central index
// entry. Tolerates missing files. Used by callers that have torn down the
// worktree (sc close, sc clean) and by abandoned-session reaping. To
// preserve close history, callers should call Tombstone instead before
// removing the worktree.
func Remove(repoPath, branch string) error {
	migrateOnce()
	wt := worktreeFromRepoBranch(repoPath, branch)
	sessionlog.Infof("session.Remove wt=%s branch=%s from=%s", wt, branch, caller(1))
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

// implicitStatePath is the worktree-local path of an implicit session's
// per-randID state file: <checkout>/.spinclass/state-<randID>.json.
func implicitStatePath(checkout, randID string) string {
	return filepath.Join(checkout, ".spinclass", "state-"+randID+".json")
}

// implicitIndexPath returns the central index entry for an implicit state
// file. Hashing the per-randID local path keeps it unique from the worktree's
// own state.json index entry.
func implicitIndexPath(localStatePath string) string {
	return filepath.Join(indexDir(), indexFilename(localStatePath))
}

// WriteImplicit persists an implicit (main-checkout) session to
// <checkout>/.spinclass/state-<randID>.json plus a central index symlink. Unlike
// Write, it keys on randID (not worktree path) so concurrent main-checkout agents
// never collide. s.WorktreePath must be the checkout root.
func WriteImplicit(s State, randID string) error {
	from := caller(1)
	checkout := s.WorktreePath
	if checkout == "" {
		sessionlog.Errorf("session.WriteImplicit rejected (empty WorktreePath) from=%s", from)
		return errors.New("session.WriteImplicit: WorktreePath required")
	}
	if _, err := os.Stat(checkout); err != nil {
		sessionlog.Errorf("session.WriteImplicit checkout-stat-failed checkout=%s branch=%s from=%s err=%v", checkout, s.Branch, from, err)
		return fmt.Errorf("session.WriteImplicit: checkout %q: %w", checkout, err)
	}
	dir := filepath.Join(checkout, ".spinclass")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		sessionlog.Errorf("session.WriteImplicit mkdir-failed dir=%s from=%s err=%v", dir, from, err)
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	local := implicitStatePath(checkout, randID)
	if err := os.WriteFile(local, data, 0o644); err != nil {
		sessionlog.Errorf("session.WriteImplicit writefile-failed path=%s from=%s err=%v", local, from, err)
		return err
	}
	if err := os.MkdirAll(indexDir(), 0o755); err != nil {
		return err
	}
	link := implicitIndexPath(local)
	tmp := filepath.Join(indexDir(), fmt.Sprintf(".tmp-%d-%d.json", os.Getpid(), time.Now().UnixNano()))
	if err := os.Symlink(local, tmp); err != nil {
		sessionlog.Errorf("session.WriteImplicit symlink-failed local=%s from=%s err=%v", local, from, err)
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		sessionlog.Errorf("session.WriteImplicit symlink-failed local=%s from=%s err=%v", local, from, err)
		return err
	}
	sessionlog.Infof("session.WriteImplicit wt=%s branch=%s state=%s pid=%d from=%s", checkout, s.Branch, s.SessionState, s.PID, from)
	return nil
}

// RemoveImplicit deletes an implicit session's per-randID state file and its
// central index entry. Tolerates missing files. Never removes the checkout's
// .spinclass dir wholesale (other agents may have siblings there).
func RemoveImplicit(checkout, randID string) error {
	sessionlog.Infof("session.RemoveImplicit checkout=%s from=%s", checkout, caller(1))
	return removeImplicitByPath(implicitStatePath(checkout, randID))
}

// removeImplicitByPath removes an implicit session's local state file at
// localStatePath and its central index entry. Used by SweepDeadImplicit, which
// already holds the matched path and need not re-derive the randID.
func removeImplicitByPath(localStatePath string) error {
	if err := os.Remove(localStatePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(implicitIndexPath(localStatePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SweepDeadImplicit removes implicit state-<rand>.json files in checkout whose
// recorded PID is no longer alive. Best-effort: a leaked file from a missed
// SessionEnd is reaped the next time any agent starts in the checkout. Errors
// reading or unmarshaling an individual file are ignored; only a failure of the
// glob itself is returned.
func SweepDeadImplicit(checkout string) error {
	pattern := filepath.Join(checkout, ".spinclass", "state-*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, m := range matches {
		data, rerr := os.ReadFile(m)
		if rerr != nil {
			continue
		}
		var s State
		if json.Unmarshal(data, &s) != nil {
			continue
		}
		if s.PID == 0 {
			// PID 0 means the session was written without a recorded PID;
			// skip rather than reap — we only delete files we can confirm
			// belong to a dead process.
			continue
		}
		if !IsAlive(s.PID) {
			_ = removeImplicitByPath(m)
		}
	}
	return nil
}

// FindImplicitAtCwd returns the live implicit session State for the main
// checkout containing cwd plus its randID (the per-session suffix of its
// state-<randID>.json file), or (nil, "", nil) if there is none. Implicit
// state lives in <checkout-root>/.spinclass/, so the lookup first globs cwd
// directly (the common case: cwd IS the checkout root) and, on a miss, climbs
// to the nearest .git root and globs there — so invocation from a checkout
// SUBDIRECTORY resolves the same session (#162). The first state-*.json whose
// recorded PID is alive wins (concurrent agents in one checkout each have
// their own file; any live one identifies "an implicit session is active
// here"). The randID lets callers address the exact state file (e.g. consuming
// a single-use attestation). Dead-PID files are ignored (swept on next
// SessionStart). Returns an error only on a glob failure.
func FindImplicitAtCwd(cwd string) (*State, string, error) {
	st, randID, found, err := findLiveImplicitInDir(cwd)
	if err != nil || found {
		return st, randID, err
	}
	// cwd had no live implicit session. If cwd is a subdirectory of a main
	// checkout, the state lives at the checkout root — climb and glob once
	// more there. DetectRepo respects GIT_CEILING_DIRECTORIES and errors on a
	// non-git dir (the legitimate not-a-session case), which we treat as
	// "nothing here" rather than propagate.
	root, derr := worktree.DetectRepo(cwd)
	if derr != nil || filepath.Clean(root) == filepath.Clean(cwd) {
		return nil, "", nil
	}
	st, randID, _, err = findLiveImplicitInDir(root)
	return st, randID, err
}

// findLiveImplicitInDir globs <dir>/.spinclass/state-*.json for the first
// live implicit session. found distinguishes a hit from a clean miss so
// callers know whether to keep searching. err is only ever a glob failure.
func findLiveImplicitInDir(dir string) (st *State, randID string, found bool, err error) {
	matches, err := filepath.Glob(filepath.Join(dir, ".spinclass", "state-*.json"))
	if err != nil {
		return nil, "", false, err
	}
	for _, m := range matches {
		data, rerr := os.ReadFile(m)
		if rerr != nil {
			continue
		}
		var s State
		if json.Unmarshal(data, &s) != nil {
			continue
		}
		if s.Kind == KindImplicit && s.PID != 0 && IsAlive(s.PID) {
			rand := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "state-"), ".json")
			return &s, rand, true, nil
		}
	}
	return nil, "", false, nil
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
	from := caller(1)
	sessionlog.Infof("session.Tombstone wt=%s branch=%s from=%s", wt, branch, from)
	statePath := worktreeStatePath(wt)
	data, err := os.ReadFile(statePath)
	if err != nil {
		sessionlog.Errorf("session.Tombstone read-failed path=%s from=%s err=%v", statePath, from, err)
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
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return werr
	}
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(tmpName)
		return cerr
	}
	if err := os.Rename(tmpName, idx); err != nil {
		_ = os.Remove(tmpName)
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

// IsTombstone reports whether this State was loaded from a tombstone
// (a regular file at the central index path) rather than from a live
// worktree-local state.json. Used by display layers that want to mark
// closed sessions specially.
func (s *State) IsTombstone() bool {
	return s.isTombstone
}

// DefaultTombstoneRetention is the fallback retention window used by
// `sc clean` when `[session-entry].tombstone-retention` is unset.
func DefaultTombstoneRetention() time.Duration {
	return 30 * 24 * time.Hour
}

// GCTombstones removes tombstone files from the central index whose
// exited_at timestamp is older than retention. retention == 0 is a
// no-op (tombstones never expire). Returns the count of files removed.
//
// Live symlinks (resolving), dangling symlinks, and tombstones with
// undecodable JSON are left alone — this function only acts on entries
// it can confidently classify and read.
func GCTombstones(retention time.Duration) (int, error) {
	if retention <= 0 {
		return 0, nil
	}
	migrateOnce()

	dir := indexDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	cutoff := time.Now().Add(-retention)
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		full := filepath.Join(dir, e.Name())
		info, lerr := os.Lstat(full)
		if lerr != nil {
			continue
		}
		// Only operate on regular files (tombstones). Symlinks (live or
		// dangling) are out of scope.
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}

		data, rerr := os.ReadFile(full)
		if rerr != nil {
			continue
		}
		var s State
		if jerr := json.Unmarshal(data, &s); jerr != nil {
			continue
		}
		if s.ExitedAt == nil || s.ExitedAt.After(cutoff) {
			continue
		}
		if rmErr := os.Remove(full); rmErr == nil {
			removed++
		}
	}
	return removed, nil
}

// ResolveState checks the actual state, handling crash recovery.
// If the session was loaded from a tombstone, returns StateAbandoned.
// If the worktree dir doesn't exist, returns StateAbandoned.
// If state file says "active" but PID is dead, returns StateInactive.
// StateRunningDetached is returned as-is — the spinclass PID is
// expected to be dead, but the multiplexer group is alive (the post-
// Attach liveness probe verified that before the state was written).
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

// ResolveDisplayState augments ResolveState with clown-presence liveness for
// `sc list`. Under the FDR-0017 posh cutover clown owns the multiplexer attach
// and names sessions by its own per-instance key, so the recorded spinclass PID
// is routinely dead even while a clown harness runs in the worktree (spawned
// workers, post-exec attaches) — ResolveState would call that inactive (#153).
// When the base state is inactive but at least one live clown is present
// (liveClowns > 0, the count of fresh clown presence records whose decoration ==
// this session's key), the session is shown running-detached: harness alive, no
// attached spinclass client. Presence never rescues an abandoned session
// (worktree gone / tombstone) — callers keep filtering on the base ResolveState.
func (s *State) ResolveDisplayState(liveClowns int) string {
	base := s.ResolveState()
	if base == StateInactive && liveClowns > 0 {
		return StateRunningDetached
	}
	return base
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

	states, err := ListAll(nil)
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

// ErrTargetNotFound tags FindByTarget misses so callers can distinguish
// "nothing matched" (close appends a bare-git-worktree hint) from an
// ambiguity error, which must surface to the user untouched.
var ErrTargetNotFound = errors.New("no session found")

// Key returns the session key (`<repo-dirname>/<branch>`, the first
// column of `sc list`), computing it from RepoPath and Branch for
// legacy state files that predate the SessionKey field.
func (s *State) Key() string {
	if s.SessionKey != "" {
		return s.SessionKey
	}
	return filepath.Base(s.RepoPath) + "/" + s.Branch
}

// FindByTarget resolves a user-supplied target to a session. Two
// grammars match: the worktree directory basename (`quiet-oak`, which
// may differ from the git branch) and the session key
// `<repo-dirname>/<branch>` exactly as `sc list` prints it. A bare
// basename matching sessions in several repos errors with the colliding
// session keys instead of arbitrarily picking one; misses are tagged
// ErrTargetNotFound.
func FindByTarget(target string) (*State, error) {
	migrateOnce()
	suffix := "/.worktrees/" + target
	states, err := ListAll(nil)
	if err != nil {
		return nil, err
	}
	var matches []*State
	for i := range states {
		s := &states[i]
		if s.Key() == target || strings.HasSuffix(s.WorktreePath, suffix) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%w for target %q", ErrTargetNotFound, target)
	case 1:
		return matches[0], nil
	}
	keys := make([]string, len(matches))
	for i, m := range matches {
		keys[i] = m.Key()
	}
	return nil, fmt.Errorf(
		"target %q is ambiguous: matches %s — use a session key",
		target, strings.Join(keys, ", "),
	)
}

// SortStates orders sessions in place: active first, then
// running-detached, then everything else (inactive, abandoned), with
// alphabetical-by-branch tie-breaking inside each tier. Both completer
// output and the interactive picker share this so callers get the same
// ordering everywhere.
func SortStates(states []State) {
	tier := func(s *State) int {
		switch s.ResolveState() {
		case StateActive:
			return 0
		case StateRunningDetached:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(states, func(i, j int) bool {
		ti, tj := tier(&states[i]), tier(&states[j])
		if ti != tj {
			return ti < tj
		}
		return states[i].Branch < states[j].Branch
	})
}

// ListForRepo returns sessions whose RepoPath matches and whose resolved
// state is not abandoned. When dbg is non-nil, every excluded entry is
// logged at Debug level with a `reason` attribute.
func ListForRepo(repoPath string, dbg *slog.Logger) ([]State, error) {
	log := debugLogger(dbg)
	all, err := ListAll(log)
	if err != nil {
		return nil, err
	}
	var filtered []State
	for i := range all {
		s := &all[i]
		if s.RepoPath != repoPath {
			log.Debug(
				"session.ListForRepo: skipped",
				"reason", "repo_mismatch",
				"want_repo", repoPath,
				"got_repo", s.RepoPath,
				"branch", s.Branch,
				"worktree", s.WorktreePath,
			)
			continue
		}
		if s.ResolveState() == StateAbandoned {
			log.Debug(
				"session.ListForRepo: skipped",
				"reason", "abandoned",
				"branch", s.Branch,
				"worktree", s.WorktreePath,
				"tombstone", s.isTombstone,
			)
			continue
		}
		filtered = append(filtered, *s)
	}
	return filtered, nil
}

// ListActiveForRepoExcluding returns the sessions on repoPath whose resolved
// state is StateActive (PID alive + worktree present), excluding any whose
// WorktreePath resolves to excludeWorktree — the caller's own session. For a
// main checkout the exclusion conservatively drops every implicit session at
// that checkout (concurrent implicit agents share the checkout path and cannot
// be told apart), leaving only worktree sessions. Results are sorted by branch
// for deterministic display. Best-effort consumers (the co-active surfacing of
// spinclass#238) treat an error as "none".
func ListActiveForRepoExcluding(repoPath, excludeWorktree string) ([]State, error) {
	all, err := ListForRepo(repoPath, nil)
	if err != nil {
		return nil, err
	}
	exclude := evalOrClean(excludeWorktree)
	var active []State
	for i := range all {
		s := &all[i]
		if s.ResolveState() != StateActive {
			continue
		}
		if evalOrClean(s.WorktreePath) == exclude {
			continue
		}
		active = append(active, *s)
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Branch < active[j].Branch })
	return active, nil
}

// BranchOrKey returns the branch name when known, falling back to the
// session key — the short display name co-active listings use (#238).
func (s *State) BranchOrKey() string {
	if s.Branch != "" {
		return s.Branch
	}
	return s.Key()
}

// ListForScope returns the non-abandoned sessions visible from dir:
// those whose RepoPath is exactly repoPath (the repo containing dir,
// when inside one) unioned with those whose RepoPath sits at or beneath
// dir after symlink/lexical normalization — so a cwd above several
// repos (e.g. ~/eng over ~/eng/repos/*) sees the nested repos'
// sessions too. Matching is path-component-aware via pathInsideResolved.
// When dbg is non-nil, every excluded entry is logged at Debug level.
func ListForScope(repoPath, dir string, dbg *slog.Logger) ([]State, error) {
	log := debugLogger(dbg)
	all, err := ListAll(log)
	if err != nil {
		return nil, err
	}
	resolvedDir := evalOrClean(dir)
	var filtered []State
	for i := range all {
		s := &all[i]
		if s.RepoPath != repoPath && !pathInsideResolved(evalOrClean(s.RepoPath), resolvedDir) {
			log.Debug(
				"session.ListForScope: skipped",
				"reason", "scope_mismatch",
				"want_repo", repoPath,
				"want_dir", dir,
				"got_repo", s.RepoPath,
				"branch", s.Branch,
				"worktree", s.WorktreePath,
			)
			continue
		}
		if s.ResolveState() == StateAbandoned {
			log.Debug(
				"session.ListForScope: skipped",
				"reason", "abandoned",
				"branch", s.Branch,
				"worktree", s.WorktreePath,
				"tombstone", s.isTombstone,
			)
			continue
		}
		filtered = append(filtered, *s)
	}
	return filtered, nil
}

// ListAll walks the central index and returns every session it can read.
// Live entries (symlinks that resolve), tombstones (regular files), and
// dangling symlinks all appear; ResolveState honours each appropriately.
// When dbg is non-nil, every index entry that is silently dropped (lstat
// failure, unreadable file, malformed JSON) is logged at Debug level.
func ListAll(dbg *slog.Logger) ([]State, error) {
	log := debugLogger(dbg)
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
			log.Debug(
				"session.ListAll: skipped index entry",
				"reason", "is_directory",
				"name", e.Name(),
			)
			continue
		}
		full := filepath.Join(dir, e.Name())

		info, lerr := os.Lstat(full)
		if lerr != nil {
			log.Debug(
				"session.ListAll: skipped index entry",
				"reason", "lstat_error",
				"name", e.Name(),
				"error", lerr.Error(),
			)
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
					log.Debug(
						"session.ListAll: dangling symlink",
						"name", e.Name(),
						"target", target,
						"read_error", rerr.Error(),
					)
					states = append(states, danglingStateFromTarget(target))
				} else {
					log.Debug(
						"session.ListAll: skipped index entry",
						"reason", "unreadable_dangling_symlink",
						"name", e.Name(),
						"read_error", rerr.Error(),
						"readlink_error", terr.Error(),
					)
				}
			} else {
				log.Debug(
					"session.ListAll: skipped index entry",
					"reason", "readfile_error",
					"name", e.Name(),
					"error", rerr.Error(),
				)
			}
			continue
		}
		var s State
		if jerr := json.Unmarshal(data, &s); jerr != nil {
			log.Debug(
				"session.ListAll: skipped index entry",
				"reason", "unmarshal_error",
				"name", e.Name(),
				"error", jerr.Error(),
			)
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
			_ = os.Remove(oldFile)
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
