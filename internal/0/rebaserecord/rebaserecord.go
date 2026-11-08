// Package rebaserecord is the frontend's record of the branch a rebase that
// grit started is replaying (spinclass#456). A rebase in progress leaves
// HEAD detached, and git names the branch in rebase-merge/head-name under the
// common git directory, a file a confined unit can write; so the permission
// classifier cannot judge ownership of a rebase on it alone. grit writes this
// record, outside every path a unit binds, at the moment it starts a rebase
// of a branch it has already judged owned, and the classifier lets a later
// continue, skip or abort through only while head-name still agrees with it.
//
// One small JSON file per worktree under
// $XDG_STATE_HOME/spinclass/rebases/, keyed by a hash of the worktree path.
package rebaserecord

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type record struct {
	Worktree string `json:"worktree"`
	Head     string `json:"head"`
}

// Dir is $XDG_STATE_HOME/spinclass/rebases, defaulting the base to
// $HOME/.local/state.
func Dir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "spinclass", "rebases")
}

func file(worktree string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(worktree)))
	return filepath.Join(Dir(), hex.EncodeToString(sum[:8])+".json")
}

// Files is the on-disk record store; the zero value is ready to use.
type Files struct{}

// Write records that the rebase in progress in worktree replays head, a full
// refs/heads/ ref. Atomic (temp file + rename).
func (Files) Write(worktree, head string) error {
	if worktree == "" || !branchRef(head) {
		return fmt.Errorf("rebase record for %q: %q is not a branch ref", worktree, head)
	}
	data, err := json.Marshal(record{Worktree: filepath.Clean(worktree), Head: head})
	if err != nil {
		return err
	}
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, file(worktree)); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// Read is the branch ref recorded for worktree, or false when there is none or
// the file is damaged or names another worktree.
func (Files) Read(worktree string) (string, bool) {
	data, err := os.ReadFile(file(worktree))
	if err != nil {
		return "", false
	}
	var r record
	if json.Unmarshal(data, &r) != nil || r.Worktree != filepath.Clean(worktree) || !branchRef(r.Head) {
		return "", false
	}
	return r.Head, true
}

// branchRef is true for a refs/heads/ ref with a non-empty name and no line
// break or NUL.
func branchRef(ref string) bool {
	name, ok := strings.CutPrefix(ref, "refs/heads/")
	return ok && name != "" && !strings.ContainsAny(name, "\n\x00")
}

// Clear removes the record for worktree; absent is fine.
func (Files) Clear(worktree string) {
	_ = os.Remove(file(worktree))
}
