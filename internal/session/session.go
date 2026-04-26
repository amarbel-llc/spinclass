package session

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
}

func stateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "spinclass", "sessions")
}

func stateFilename(repoPath, branch string) string {
	h := sha256.Sum256([]byte(repoPath + "/" + branch))
	return fmt.Sprintf("%x-state.json", h[:8])
}

func statePath(repoPath, branch string) string {
	return filepath.Join(stateDir(), stateFilename(repoPath, branch))
}

func Write(s State) error {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(s.RepoPath, s.Branch), data, 0o644)
}

func Read(repoPath, branch string) (*State, error) {
	data, err := os.ReadFile(statePath(repoPath, branch))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func Remove(repoPath, branch string) error {
	return os.Remove(statePath(repoPath, branch))
}

func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

// ResolveState checks the actual state, handling crash recovery.
// If state file says "active" but PID is dead, returns StateInactive.
// If worktree dir doesn't exist, returns StateAbandoned.
func (s *State) ResolveState() string {
	if _, err := os.Stat(s.WorktreePath); os.IsNotExist(err) {
		return StateAbandoned
	}
	if s.SessionState == StateActive && !IsAlive(s.PID) {
		return StateInactive
	}
	return s.SessionState
}

// FindByWorktreePath scans all session state files and returns the
// session whose WorktreePath is path or contains it. Symlinks on either
// side are resolved before comparison so a symlink-backed cwd matches a
// real worktree, and component-aware matching prevents `/foo/bar` from
// matching the unrelated `/foo/bar-baz`.
func FindByWorktreePath(path string) (*State, error) {
	states, err := ListAll()
	if err != nil {
		return nil, err
	}
	resolvedPath := evalOrClean(path)
	for i := range states {
		s := &states[i]
		if pathInsideResolved(resolvedPath, evalOrClean(s.WorktreePath)) {
			return s, nil
		}
	}
	return nil, fmt.Errorf("no session found for path %s", path)
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

// FindByID scans all session state files and returns the one whose
// WorktreePath ends in /.worktrees/<id>. The id is the worktree directory
// name, which may differ from the git branch.
func FindByID(id string) (*State, error) {
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

func ListAll() ([]State, error) {
	dir := stateDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
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
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s State
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}
		states = append(states, s)
	}
	return states, nil
}
