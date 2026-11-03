package check

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The guarded gate's lock policy (spinclass#449, review finding F1). The nix
// evaluator runs as the operator uid with the whole filesystem readable, and a
// flake input it fetches is copied into /nix/store, world-readable and mounted
// into every unit, before the lock's narHash is compared. A worker-committed
// lock naming `path:/home/<operator>/...` with a wrong hash therefore leaks that
// path through one failed merge (shown by `just explore-guarded-gate-input-leak`).
// A guarded gate refuses, before anything runs, a lock with any node that
// points at a local path. Policy, not a boundary: the boundary is evaluation
// in a mount namespace that sees only the build worktree (spinclass#448).

// flakeLock is the part of flake.lock the policy reads.
type flakeLock struct {
	Nodes map[string]struct {
		Locked struct {
			Type string `json:"type"`
			URL  string `json:"url"`
			Path string `json:"path"`
		} `json:"locked"`
		Original struct {
			Type string `json:"type"`
			URL  string `json:"url"`
			Path string `json:"path"`
		} `json:"original"`
	} `json:"nodes"`
	Root string `json:"root"`
}

// refuseUnsafeFlakeLock returns an error naming every lock node in dir's
// flake.lock that names a local path: type `path`, or any URL with a file
// scheme (`file:`, `git+file:`, and the `+file` forms). The refusal names
// the node and the kind only, never the path or URL or a hash: the merge
// record reaches the worker, and the path is what the worker is after. A
// flake.nix with no flake.lock is refused too: nix refuses it as well under
// --no-update-lock-file, before fetching anything (shown by the PoC recipe),
// and so does nix a flake.nix input the lock lacks; the policy does not parse
// flake.nix itself. A directory with no flake.nix has no lock to judge.
func refuseUnsafeFlakeLock(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "flake.nix")); err != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "flake.lock"))
	if err != nil {
		return fmt.Errorf("a guarded gate needs a committed flake.lock beside flake.nix, and there is none (%w); nothing ran", err)
	}
	var lock flakeLock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return fmt.Errorf("a guarded gate could not read flake.lock: %w; nothing ran", err)
	}
	var bad []string
	for name, node := range lock.Nodes {
		if name == lock.Root || name == "root" {
			continue
		}
		for _, ref := range []struct{ typ, url, path string }{
			{node.Locked.Type, node.Locked.URL, node.Locked.Path},
			{node.Original.Type, node.Original.URL, node.Original.Path},
		} {
			if reason := localPathInput(ref.typ, ref.url, ref.path); reason != "" {
				bad = append(bad, name+": "+reason)
				break
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return errors.New("the flake.lock names a flake input on the local filesystem, which the evaluator would copy into the world-readable /nix/store as the operator before the gate could refuse it; a guarded merge accepts only remote inputs; nothing ran\n  " + strings.Join(bad, "\n  "))
}

// localPathInput says why an input reference is a local path, or "". It
// names the kind, never the path or URL.
func localPathInput(typ, url, path string) string {
	if typ == "path" {
		return "type path"
	}
	u := strings.ToLower(url)
	switch {
	case strings.HasPrefix(u, "file:"), strings.HasPrefix(u, "path:"), strings.Contains(u, "+file:"):
		return fmt.Sprintf("type %s with a file url", typ)
	}
	if path != "" && typ != "" {
		return fmt.Sprintf("type %s with a path", typ)
	}
	return ""
}
