// Package direnv centralizes how spinclass locates and invokes direnv, so the
// resolution rule and the `direnv exec` argv shape live in one place rather than
// being hand-mirrored across the packages that devshell-scope a command
// (internal/sweatfile hooks, internal/sessionexec). See spinclass#199.
//
// Caller-specific policy stays with the caller: whether to wrap at all (e.g.
// sweatfile gates on a real .envrc), and which directory's devshell to load
// versus where the command runs (the hook's envDir/runDir split). This package
// only answers "where is direnv" and "what argv runs <cmd> under it".
package direnv

import (
	"os/exec"

	"code.linenisgreat.com/spinclass/internal/embeds"
)

// Resolve returns the absolute path to the direnv binary, preferring the
// build-time pin (from `lib.mkSpinclass`) over a PATH lookup. Returns
// ("", false) when direnv is unavailable in either location — callers treat
// that as "no direnv, run the command directly".
func Resolve() (string, bool) {
	if pinned := embeds.DirenvBin(); pinned != "" {
		return pinned, true
	}
	path, err := exec.LookPath("direnv")
	if err != nil {
		return "", false
	}
	return path, true
}

// WrapExec returns argv prefixed with `direnv exec <dir>` so cmd runs under the
// devshell loaded from dir's .envrc. `direnv exec DIR COMMAND` loads the .envrc
// found in DIR but does not change the working directory, so the caller's
// cmd.Dir still governs where the command runs (the basis of the hook
// envDir/runDir split). direnvPath is the value returned by Resolve.
func WrapExec(direnvPath, dir string, cmd []string) []string {
	return append([]string{direnvPath, "exec", dir}, cmd...)
}
