package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// hookTraceName is the worktree-local trace of the spawn SessionStart hook. The
// hook's lifecycle log lives in the state dir, which a confined agent unit does
// not share with the frontend, so a hook that fails inside a unit leaves
// nothing the operator can read. The worktree's .spinclass dir is bound
// read-write and visible from the host, so a short trace there is the evidence
// path (the first real confined worker's hello never arrived and nothing said
// why).
const hookTraceName = "hook-trace.log"

// hookTraceMaxBytes caps the trace so a hook that fires on every resume cannot
// grow it without bound.
const hookTraceMaxBytes = 64 << 10

// hookTrace appends one timestamped line to <cwd>/.spinclass/hook-trace.log.
// Best-effort and silent on any failure: a hook must never block startup, and
// it does not create .spinclass (a worktree spinclass never set up has none).
func hookTrace(cwd, format string, args ...any) {
	dir := filepath.Join(cwd, ".spinclass")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return
	}
	path := filepath.Join(dir, hookTraceName)
	if fi, err := os.Stat(path); err == nil && fi.Size() > hookTraceMaxBytes {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s pid=%d %s\n", time.Now().UTC().Format(time.RFC3339Nano), os.Getpid(), fmt.Sprintf(format, args...))
}
