package sweatfileio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// ParseKeyholderMode turns a [keyholder] mode value into the switch: "on" is
// true, "off" and the empty string are false, anything else is an error naming
// the value. It is the one parser the resolver and `sc validate` share.
func ParseKeyholderMode(v string) (bool, error) {
	switch v {
	case "", "off":
		return false, nil
	case "on":
		return true, nil
	}
	return false, fmt.Errorf("[keyholder] mode %q is not valid: use \"off\" or \"on\"", v)
}

// TrustedKeyholderMode returns the [keyholder] mode honoured for the repo: a
// scalar, the last layer above the repo that sets it wins, off when unset. The
// key holder replaces what a session can sign with, so it is the operator's
// switch: layers that may not set it (the repo and worktree files) are never
// honoured, and their paths are returned in untrusted (any [keyholder] key
// there counts, the paths too). An invalid value in a trusted layer is an
// error and the answer stays off.
func TrustedKeyholderMode(h sweatfile.Hierarchy, home, repoRoot string) (on bool, untrusted []string, err error) {
	var value string
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.Keyholder == nil {
			continue
		}
		if !LayerAboveRepo(src.Path, home, repoRoot) {
			untrusted = append(untrusted, src.Path)
			continue
		}
		if src.File.Keyholder.Mode != nil {
			value = *src.File.Keyholder.Mode
		}
	}
	on, err = ParseKeyholderMode(value)
	if err != nil {
		return false, untrusted, err
	}
	return on, untrusted, nil
}

// TrustedKeyholderTools returns the two program paths the pair runs, as the
// host's configuration wrote them: [keyholder] fibby and piggy, each the last
// value a layer ABOVE the repo sets (a repo or worktree layer is never honoured
// and is returned in untrusted). Unset is the empty string; the values are NOT
// checked here: CheckKeyholderToolPath is what refuses a bad one, and only when
// the mode is on.
func TrustedKeyholderTools(h sweatfile.Hierarchy, home, repoRoot string) (fibby, piggy string, untrusted []string) {
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found || src.File.Keyholder == nil {
			continue
		}
		if !LayerAboveRepo(src.Path, home, repoRoot) {
			untrusted = append(untrusted, src.Path)
			continue
		}
		if v := src.File.Keyholder.Fibby; v != nil {
			fibby = *v
		}
		if v := src.File.Keyholder.Piggy; v != nil {
			piggy = *v
		}
	}
	return fibby, piggy, untrusted
}

// KeyholderStoreRoot is the directory a pair's programs must live under. A var
// only so a test can stand a temporary directory in for the Nix store.
var KeyholderStoreRoot = "/nix/store"

// CheckKeyholderToolPath refuses a [keyholder] fibby or piggy value that is not
// a program the pair may run: set, absolute, clean, under the store root, and,
// once every link is followed, still under it and an executable regular file.
// The pair holds a session's key and runs outside the unit, so a path a session
// could rewrite, or a name resolved from a PATH it can influence, is the hole;
// the error names the setting and the reason.
func CheckKeyholderToolPath(name, path string) error {
	setting := "[keyholder] " + name
	if path == "" {
		return fmt.Errorf("%s is not set: the host's configuration writes the store path of the %s the host runs", setting, name)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s %q is not an absolute path", setting, path)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("%s %q is not clean (no .., //, or trailing /)", setting, path)
	}
	root := filepath.Clean(KeyholderStoreRoot)
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return fmt.Errorf("%s %q is not under %s", setting, path, root)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("%s %q cannot be read: %v", setting, path, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("%s: the store root %s cannot be read: %v", setting, root, err)
	}
	if !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
		return fmt.Errorf("%s %q resolves to %q, which leaves %s", setting, path, resolved, root)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("%s %q cannot be read: %v", setting, path, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s %q is not a regular file", setting, path)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s %q is not executable", setting, path)
	}
	return nil
}
