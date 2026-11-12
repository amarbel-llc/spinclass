package confinement

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// ProtectedStateDirs are the directories under the XDG state base that hold
// operator-only credentials and so must never be bound into, or be reachable
// from, an agent unit (spinclass#431). circus keeps the issue-filer service
// credential under circus/. A confined unit keeps the operator's uid, so file
// modes do not keep it out; ProtectHome hiding the home and the spec not binding
// the directory does. Add a name here to protect another directory.
// spinclass/token-pool holds the pre-minted forge token pool (FDR 0028 "Token
// pool"): bearer tokens for every repo in the batch.
// spinclass/host-queue holds the host admission queue's hold file, journal and
// cost history (FDR 0038, spinclass#483): a unit that could write them could
// hold the queue or forge the audit trail. Its runtime half, the ledger under
// $XDG_RUNTIME_DIR/spinclass/host-queue, is kept out by F14, whose allowlist of
// the operator's runtime dir does not name spinclass.
var ProtectedStateDirs = []string{"circus", "spinclass/token-pool", "spinclass/host-queue"}

// ProtectedPaths returns the protected directories under stateDir.
func ProtectedPaths(stateDir string) []string {
	out := make([]string, 0, len(ProtectedStateDirs))
	for _, d := range ProtectedStateDirs {
		out = append(out, filepath.Join(stateDir, d))
	}
	return out
}

// resolvePath cleans p and resolves symlinks in its longest existing prefix; a
// missing tail is appended unresolved.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return p
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// pathExposes reports whether a bind at bind makes prot reachable: the same
// path, a path inside it, or an ancestor of it. Both must be resolved.
func pathExposes(bind, prot string) bool {
	if bind == prot || bind == "/" {
		return true
	}
	return strings.HasPrefix(bind, prot+"/") || strings.HasPrefix(prot, bind+"/")
}

// ExposedProtectedPath returns the first bind that exposes a protected
// directory under stateDir, and the directory it exposes. Paths are compared
// cleaned and symlink-resolved, by path component (circus-other is not circus).
func ExposedProtectedPath(stateDir string, binds []string) (bind, protected string, exposed bool) {
	prots := ProtectedPaths(stateDir)
	for _, b := range binds {
		rb := resolvePath(b)
		for _, p := range prots {
			if pathExposes(rb, resolvePath(p)) {
				return b, p, true
			}
		}
	}
	return "", "", false
}

// RefuseProtectedBinds is the error for a spec whose binds expose a protected
// directory, nil when none does.
func RefuseProtectedBinds(stateDir string, binds []string) error {
	if b, p, bad := ExposedProtectedPath(stateDir, binds); bad {
		return fmt.Errorf("refusing a unit spec that binds %s: it exposes the protected %s (spinclass#431)", b, p)
	}
	return nil
}

// UnitSpecBinds returns every path the launch spec binds or exposes into the
// unit: binds_rw, binds_ro and the sockets.
func UnitSpecBinds(b []byte) ([]string, error) {
	var s struct {
		BindsRW []string `json:"binds_rw"`
		BindsRO []string `json:"binds_ro"`
		Sockets []struct {
			Path string `json:"path"`
		} `json:"sockets"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("unit spec does not parse: %w", err)
	}
	out := append(append([]string{}, s.BindsRW...), s.BindsRO...)
	for _, sk := range s.Sockets {
		out = append(out, sk.Path)
	}
	return out, nil
}
