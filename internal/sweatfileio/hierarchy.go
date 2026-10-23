package sweatfileio

import (
	"fmt"
	"os"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/git"
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
//
// repoDir inside a linked git worktree is first resolved to the MAIN checkout
// (mainCheckoutRoot), so a worktree's own sweatfile is never a layer.
func LoadHierarchy(home, repoDir string) (sweatfile.Hierarchy, error) {
	return loadHierarchy(home, repoDir, true)
}

// loadHierarchy is LoadHierarchy; withRepo false stops before step 4, leaving
// the repo layer to a caller that supplies it from a committed blob.
func loadHierarchy(home, repoDir string, withRepo bool) (sweatfile.Hierarchy, error) {
	repoDir = mainCheckoutRoot(filepath.Clean(repoDir))
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
	if withRepo {
		if err := addSource(cleanRepo); err != nil {
			return sweatfile.Hierarchy{}, err
		}
		if realRepo != cleanRepo {
			if err := addSource(realRepo); err != nil {
				return sweatfile.Hierarchy{}, err
			}
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

// mainCheckoutRoot resolves dir to the main checkout root when dir is, or sits
// inside, a LINKED git worktree; any other dir (a plain directory, a main
// checkout or a subdirectory of one) comes back unchanged.
//
// A worktree is writable by the worker it hosts, including a confined one, so
// nothing a worker can write may be read as sweatfile policy: the worktree's own
// `sweatfile` and its `.worktrees/` ancestor chain must never become layers.
// Resolving here, in the loader every caller funnels through, closes that for
// all of them at once, from any cwd. Only the filesystem is consulted (no git
// subprocess) so the answer needs nothing a corrupt worktree could break. The
// common dir comes from the read-only .git pointer's layout, never the
// worker-writable commondir file (git.LayoutCommonDir); a pointer that does not
// resolve to a `<main>/.git` common dir (a submodule, say) leaves dir as it was.
func mainCheckoutRoot(dir string) string {
	if common, ok := git.LayoutCommonDir(dir); ok && filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return dir
}

// Overrides are the worktree-local settings a worktree's own sweatfile may still
// carry. It is an allow-list: a field belongs here only if it is pure data that
// cannot exec a command, name a path or weaken a policy, because the worktree is
// writable by the worker it hosts. Everything else a worktree sweatfile says is
// ignored; see mainCheckoutRoot.
type Overrides struct {
	// DisableNixGC overrides [hooks].disable-nix-gc for this worktree's own
	// close-time gc. A worker choosing to keep its own gc roots is harmless.
	DisableNixGC *bool
}

// WorktreeOverrides reads the allow-listed Overrides from <dir>/sweatfile,
// best-effort: a missing, unreadable or unparseable file yields zero Overrides
// (never an error), so a corrupt worktree file cannot change anything.
func WorktreeOverrides(dir string) Overrides {
	doc, err := Load(filepath.Join(dir, "sweatfile"))
	if err != nil {
		return Overrides{}
	}
	var o Overrides
	if hooks := doc.Data().Hooks; hooks != nil {
		o.DisableNixGC = hooks.DisableNixGC
	}
	return o
}

// LoadCommittedHierarchy loads the cascade for mainRepoRoot with its REPO layer
// taken from the sweatfile committed at rev, INSTEAD of the main checkout's
// sweatfile on disk. Global and parent layers stay live: they are the
// operator's files, outside the repository. A live, uncommitted edit of the
// main checkout's sweatfile does not apply. label names the committed layer in
// Sources. A rev with no sweatfile leaves the repo layer empty; a rev that
// cannot be read is an error.
func LoadCommittedHierarchy(home, mainRepoRoot, rev, label string) (sweatfile.Hierarchy, error) {
	data, found, err := git.FileAtRev(mainRepoRoot, rev, "sweatfile")
	if err != nil {
		return sweatfile.Hierarchy{}, fmt.Errorf("read sweatfile at %s: %w", label, err)
	}
	hierarchy, err := loadHierarchy(home, mainRepoRoot, false)
	if err != nil {
		return sweatfile.Hierarchy{}, err
	}
	return withTopLayer(hierarchy, label+":sweatfile", data, found)
}

// LoadHierarchyWithLayer is LoadHierarchy plus one caller-supplied top layer
// whose bytes come from somewhere other than a file on disk (e.g. a committed
// blob, #300). label names the layer in Sources. When !found the layer is inert,
// like a missing file: it is recorded but neither parsed nor merged, so a parse
// error surfaces only for a layer that exists.
func LoadHierarchyWithLayer(
	home, mainRepoRoot, label string, data []byte, found bool,
) (sweatfile.Hierarchy, error) {
	hierarchy, err := LoadHierarchy(home, mainRepoRoot)
	if err != nil {
		return sweatfile.Hierarchy{}, err
	}
	return withTopLayer(hierarchy, label, data, found)
}

func withTopLayer(hierarchy sweatfile.Hierarchy, label string, data []byte, found bool) (sweatfile.Hierarchy, error) {
	var sf sweatfile.Sweatfile
	if found {
		doc, err := Parse(data)
		if err != nil {
			return sweatfile.Hierarchy{}, fmt.Errorf("%s: %w", label, err)
		}
		sf = *doc.Data()
	}

	hierarchy.Sources = append(hierarchy.Sources, sweatfile.LoadSource{
		Path: label, Found: found, File: sf,
	})
	if found {
		hierarchy.Merged = hierarchy.Merged.MergeWith(sf)
	}

	return hierarchy, nil
}
