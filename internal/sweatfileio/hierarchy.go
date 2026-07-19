package sweatfileio

import (
	"fmt"
	"os"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

func LoadDefaultHierarchy() (sweatfile.Hierarchy, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return sweatfile.Hierarchy{}, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return sweatfile.Hierarchy{}, err
	}

	return LoadHierarchy(home, cwd)
}

// LoadHierarchy walks the sweatfile cascade for a given repo dir and merges
// the discovered files. The cascade is:
//
//  1. Global: $HOME/.config/spinclass/sweatfile
//  2. Lexical chain: ancestors of repoDir, root-first.
//  3. Realpath chain (only if realpath(repoDir) differs from repoDir):
//     ancestors of realpath(repoDir), root-first.
//  4. Repo sweatfile: <repoDir>/sweatfile (and <realpath(repoDir)>/sweatfile
//     if different).
//
// Within a chain the walk is bounded above by $HOME (exclusive — loaded as
// global) when the chain runs through home, otherwise by the filesystem root
// (exclusive — convention is no /sweatfile). The walk is bounded below by
// the chain's own end (exclusive — repo sweatfile loaded separately).
//
// Candidate parent directories are deduplicated by canonical path: when two
// paths in either chain resolve to the same canonical directory, only the
// first is loaded; the second is recorded with SkipReason set so callers
// (e.g. validate) can surface the dedup decision.
func LoadHierarchy(home, repoDir string) (sweatfile.Hierarchy, error) {
	var sources []sweatfile.LoadSource
	merged := sweatfile.Sweatfile{}
	seenCanonical := make(map[string]bool)

	addSource := func(dir string) error {
		sweatfilePath := filepath.Join(dir, "sweatfile")
		canonical := canonicalDir(dir)
		if seenCanonical[canonical] {
			sources = append(sources, sweatfile.LoadSource{
				Path:       sweatfilePath,
				Found:      false,
				SkipReason: fmt.Sprintf("already loaded via %s", canonical),
			})
			return nil
		}
		seenCanonical[canonical] = true

		doc, err := Load(sweatfilePath)
		if err != nil {
			return err
		}
		sf := *doc.Data()
		found := fileExists(sweatfilePath)
		sources = append(sources, sweatfile.LoadSource{
			Path:  sweatfilePath,
			Found: found,
			File:  sf,
		})
		if found {
			merged = merged.MergeWith(sf)
		}
		return nil
	}

	// 1. Global config (loaded directly; not part of dedup so a sweatfile at
	// $HOME/sweatfile would still be skipped — see chainAncestors).
	globalPath := filepath.Join(home, ".config", "spinclass", "sweatfile")
	{
		doc, err := Load(globalPath)
		if err != nil {
			return sweatfile.Hierarchy{}, err
		}
		sf := *doc.Data()
		found := fileExists(globalPath)
		sources = append(
			sources,
			sweatfile.LoadSource{Path: globalPath, Found: found, File: sf},
		)
		if found {
			merged = merged.MergeWith(sf)
		}
	}

	cleanHome := filepath.Clean(home)
	cleanRepo := filepath.Clean(repoDir)
	realRepo := canonicalDir(cleanRepo)

	// 2. Lexical chain.
	for _, parentDir := range chainAncestors(cleanRepo, cleanHome) {
		if err := addSource(parentDir); err != nil {
			return sweatfile.Hierarchy{}, err
		}
	}

	// 3. Realpath chain (only if it differs).
	if realRepo != cleanRepo {
		for _, parentDir := range chainAncestors(realRepo, cleanHome) {
			if err := addSource(parentDir); err != nil {
				return sweatfile.Hierarchy{}, err
			}
		}
	}

	// 4. Repo sweatfile (lexical and realpath — dedup collapses when the
	// repo dir itself is a symlink to its realpath).
	if err := addSource(cleanRepo); err != nil {
		return sweatfile.Hierarchy{}, err
	}
	if realRepo != cleanRepo {
		if err := addSource(realRepo); err != nil {
			return sweatfile.Hierarchy{}, err
		}
	}

	return sweatfile.Hierarchy{
		Sources: sources,
		Merged:  merged,
	}, nil
}

// chainAncestors returns ancestors of `start` in root-first order, excluding
// `start` itself, the filesystem root `/`, and `homeDir`. The walk stops
// climbing when it would emit `homeDir` or `/`, so for under-home starts the
// returned slice ends at the most-specific descendant of homeDir, and for
// out-of-home starts it ends at the most-specific descendant of /.
func chainAncestors(start, homeDir string) []string {
	var ancestors []string
	d := start
	for {
		parent := filepath.Dir(d)
		if parent == d {
			break // hit root; nothing left
		}
		if parent == "/" {
			break // skip /sweatfile by convention
		}
		if parent == homeDir {
			break // skip $HOME (loaded as global)
		}
		ancestors = append([]string{parent}, ancestors...)
		d = parent
	}
	return ancestors
}

// canonicalDir returns the symlink-resolved absolute path of dir, falling
// back to dir itself when resolution fails (e.g. dir does not exist). The
// fallback is intentional: dedup is best-effort, not a correctness guarantee.
func canonicalDir(dir string) string {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return resolved
}

// LoadWorktreeHierarchy loads the sweatfile cascade for a worktree context.
// It delegates to LoadHierarchy for global → intermediate dirs → main repo,
// then appends the worktree's own sweatfile as the highest-priority layer.
func LoadWorktreeHierarchy(
	home, mainRepoRoot, worktreeDir string,
) (sweatfile.Hierarchy, error) {
	hierarchy, err := LoadHierarchy(home, mainRepoRoot)
	if err != nil {
		return sweatfile.Hierarchy{}, err
	}

	worktreePath := filepath.Join(filepath.Clean(worktreeDir), "sweatfile")
	doc, err := Load(worktreePath)
	if err != nil {
		return sweatfile.Hierarchy{}, err
	}
	sf := *doc.Data()

	found := fileExists(worktreePath)
	hierarchy.Sources = append(hierarchy.Sources, sweatfile.LoadSource{
		Path: worktreePath, Found: found, File: sf,
	})
	if found {
		hierarchy.Merged = hierarchy.Merged.MergeWith(sf)
	}

	return hierarchy, nil
}
