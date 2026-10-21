package git

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Hardening is extra environment and leading `-c` arguments every git run in a
// registered directory receives. It exists for the frontend's git on a confined
// session's behalf (FDR 0035, FDR 0036 section 9): the directory is writable
// from the confined unit, so git must not discover the repository or run
// config-defined commands from it. A directory nothing registered gets no
// hardening, and the runners below behave exactly as before.
type Hardening struct {
	Env  []string
	Args []string
}

type hardenEntry struct {
	h    Hardening
	refs int
}

var hardened = struct {
	mu sync.Mutex
	m  map[string]*hardenEntry
}{m: map[string]*hardenEntry{}}

// hardenKeys is dir cleaned, plus its symlink-resolved form when that differs,
// so a caller that spells the path either way is matched.
func hardenKeys(dir string) []string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	keys := []string{abs}
	if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
		keys = append(keys, real)
	}
	return keys
}

// Confine registers h for dir and everything beneath it until the returned
// release is called. Registrations nest by reference count: the same session's
// concurrent calls share one entry.
func Confine(dir string, h Hardening) (release func()) {
	keys := hardenKeys(dir)
	hardened.mu.Lock()
	for _, k := range keys {
		if e, ok := hardened.m[k]; ok {
			e.refs++
			e.h = h
		} else {
			hardened.m[k] = &hardenEntry{h: h, refs: 1}
		}
	}
	hardened.mu.Unlock()
	return func() {
		hardened.mu.Lock()
		defer hardened.mu.Unlock()
		for _, k := range keys {
			if e, ok := hardened.m[k]; ok {
				if e.refs--; e.refs <= 0 {
					delete(hardened.m, k)
				}
			}
		}
	}
}

// HardeningFor is the hardening of the longest registered directory that is
// repoPath or an ancestor of it.
func HardeningFor(repoPath string) (Hardening, bool) {
	hardened.mu.Lock()
	defer hardened.mu.Unlock()
	if len(hardened.m) == 0 {
		return Hardening{}, false
	}
	best, found := "", false
	var h Hardening
	for _, k := range hardenKeys(repoPath) {
		for reg, e := range hardened.m {
			if (k == reg || strings.HasPrefix(k, reg+string(filepath.Separator))) && (!found || len(reg) > len(best)) {
				best, h, found = reg, e.h, true
			}
		}
	}
	return h, found
}

// invocation is the argument list after the program name and the environment
// to use for a git run in repoPath. An unregistered repoPath yields exactly
// what the runners always built: no env override unless the caller gave one.
func invocation(repoPath string, env, args []string) (argv, cmdEnv []string) {
	h, ok := HardeningFor(repoPath)
	argv = []string{"-C", repoPath}
	if ok {
		argv = append(argv, h.Args...)
	}
	argv = append(argv, args...)
	if len(env) > 0 || (ok && len(h.Env) > 0) {
		cmdEnv = append(os.Environ(), env...)
		if ok {
			cmdEnv = append(cmdEnv, h.Env...)
		}
	}
	return argv, cmdEnv
}
