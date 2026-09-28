package merge

import (
	"os"
	"path/filepath"
	"strconv"
)

// postMergeTmpDir returns the TMPDIR every post-merge command runs with
// (spinclass#330), creating it (mode 0700) if needed.
//
// It deliberately never derives from the inherited TMPDIR. The merging process
// usually inherits a session worktree's .tmp, and the post-merge phase can
// outlive that session: on the out-of-session path (sc run, sc merge from
// outside), teardown removes the worktree BEFORE post-merge runs. Even a live
// session's .tmp can vanish under a detached post-merge child (FDR 0023). So the
// dir is per-user and session-independent, and it is shared and never removed,
// rather than per-merge: a detached child keeps its scratch space, and nothing
// accumulates.
//
// Root: $XDG_RUNTIME_DIR/spinclass/post-merge when XDG_RUNTIME_DIR is an existing
// dir, else /tmp/spinclass-post-merge-<uid>. On failure it returns "" and the
// caller keeps the inherited environment.
func postMergeTmpDir() string {
	// Not os.TempDir(): that reads $TMPDIR, the very value being avoided.
	dir := filepath.Join("/tmp", "spinclass-post-merge-"+strconv.Itoa(os.Getuid()))
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		if info, err := os.Stat(rt); err == nil && info.IsDir() {
			dir = filepath.Join(rt, "spinclass", "post-merge")
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return dir
}
