package session

import (
	"path/filepath"
	"strings"
)

// IdentityEnv is the spinclass-owned identity environment of the session with
// key ("repo/branch") at worktree, as KEY=VALUE pairs in a fixed order:
// SPINCLASS_SESSION_ID, _REPO, _BRANCH, _WORKTREE and _DESCRIPTION. It is the
// one definition the session entrypoint (executor), `sc exec` (sessionexec)
// and spawned workers (spawn, including a confined worker's unit spec) share
// (#192); callers append it last so it wins over inherited or configured
// values.
func IdentityEnv(key, worktree, description string) []string {
	repo, branch, _ := strings.Cut(key, "/")
	return []string{
		"SPINCLASS_SESSION_ID=" + key,
		"SPINCLASS_REPO=" + repo,
		"SPINCLASS_BRANCH=" + branch,
		"SPINCLASS_WORKTREE=" + worktree,
		"SPINCLASS_DESCRIPTION=" + description,
	}
}

// TmpDirEnv points TMPDIR and CLAUDE_CODE_TMPDIR at the worktree's .tmp, the
// session-local scratch space that goes away with the worktree.
func TmpDirEnv(worktree string) []string {
	tmpDir := filepath.Join(worktree, ".tmp")
	return []string{"TMPDIR=" + tmpDir, "CLAUDE_CODE_TMPDIR=" + tmpDir}
}
