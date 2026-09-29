package sweatfileio

import (
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// LayerAboveRepo reports whether the sweatfile layer at layerPath sits above
// the repo: it is the global sweatfile, or its directory is a strict ancestor
// of repoRoot. Lexical dir is compared with the cleaned root and canonical
// dir with the canonical root, never crosswise. Label layers (non-absolute
// paths such as a committed-blob label) are never above (#335).
//
// The global exemption is a path-equality check, so the global sweatfile is
// trusted even if $HOME lies inside the repo. "/" is never a layer dir
// (chainAncestors never emits it).
func LayerAboveRepo(layerPath, home, repoRoot string) bool {
	if layerPath == filepath.Join(home, ".config", "spinclass", "sweatfile") {
		return true
	}
	if !filepath.IsAbs(layerPath) {
		return false
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return false
	}
	dir := filepath.Dir(layerPath)
	return strictAncestor(dir, absRoot) ||
		strictAncestor(canonicalDir(dir), canonicalDir(absRoot))
}

func strictAncestor(dir, root string) bool {
	rel, err := filepath.Rel(dir, root)
	if err != nil || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// TrustedURLResolver returns the [auth].url-resolver command honoured for the
// repo: scalar override across layers above the repo, last one wins, an
// explicit "" clearing it. The value is TrimSpaced, so whitespace-only also
// clears. Layers that may not set it (repo, its realpath twin, worktree) are
// never honoured; the paths of those that set the key are returned in
// untrusted so callers can surface them. Hierarchy.Merged never carries the
// key.
func TrustedURLResolver(h sweatfile.Hierarchy, home, repoRoot string) (command string, untrusted []string) {
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.Auth == nil || src.File.Auth.URLResolver == nil {
			continue
		}
		if LayerAboveRepo(src.Path, home, repoRoot) {
			command = strings.TrimSpace(*src.File.Auth.URLResolver)
		} else {
			untrusted = append(untrusted, src.Path)
		}
	}
	return command, untrusted
}
