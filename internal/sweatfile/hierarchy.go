package sweatfile

import (
	"fmt"
	"os"
	"path/filepath"
)

type LoadSource struct {
	Path       string
	Found      bool
	File       Sweatfile
	SkipReason string
}

type Hierarchy struct {
	Sources []LoadSource
	Merged  Sweatfile
}

func LoadDefaultHierarchy() (Hierarchy, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return Hierarchy{}, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return Hierarchy{}, err
	}

	hierarchy, err := LoadHierarchy(home, cwd)
	if err != nil {
		return hierarchy, err
	}

	return hierarchy, nil
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
func LoadHierarchy(home, repoDir string) (Hierarchy, error) {
	var sources []LoadSource
	merged := Sweatfile{}
	seenCanonical := make(map[string]bool)

	addSource := func(dir string) error {
		sweatfilePath := filepath.Join(dir, "sweatfile")
		canonical := canonicalDir(dir)
		if seenCanonical[canonical] {
			sources = append(sources, LoadSource{
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
		_, found := fileExists(sweatfilePath)
		sources = append(sources, LoadSource{
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
			return Hierarchy{}, err
		}
		sf := *doc.Data()
		_, found := fileExists(globalPath)
		sources = append(
			sources,
			LoadSource{Path: globalPath, Found: found, File: sf},
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
			return Hierarchy{}, err
		}
	}

	// 3. Realpath chain (only if it differs).
	if realRepo != cleanRepo {
		for _, parentDir := range chainAncestors(realRepo, cleanHome) {
			if err := addSource(parentDir); err != nil {
				return Hierarchy{}, err
			}
		}
	}

	// 4. Repo sweatfile (lexical and realpath — dedup collapses when the
	// repo dir itself is a symlink to its realpath).
	if err := addSource(cleanRepo); err != nil {
		return Hierarchy{}, err
	}
	if realRepo != cleanRepo {
		if err := addSource(realRepo); err != nil {
			return Hierarchy{}, err
		}
	}

	return Hierarchy{
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
) (Hierarchy, error) {
	hierarchy, err := LoadHierarchy(home, mainRepoRoot)
	if err != nil {
		return Hierarchy{}, err
	}

	worktreePath := filepath.Join(filepath.Clean(worktreeDir), "sweatfile")
	doc, err := Load(worktreePath)
	if err != nil {
		return Hierarchy{}, err
	}
	sf := *doc.Data()

	_, found := fileExists(worktreePath)
	hierarchy.Sources = append(hierarchy.Sources, LoadSource{
		Path: worktreePath, Found: found, File: sf,
	})
	if found {
		hierarchy.Merged = hierarchy.Merged.MergeWith(sf)
	}

	return hierarchy, nil
}

func (sf Sweatfile) MergeWith(other Sweatfile) Sweatfile {
	merged := sf

	// [claude]
	if other.Claude != nil {
		if merged.Claude == nil {
			merged.Claude = &Claude{}
		}
		// allow: nil=inherit, empty=clear, non-empty=append
		if other.Claude.Allow != nil {
			if len(other.Claude.Allow) == 0 {
				merged.Claude.Allow = []string{}
			} else {
				merged.Claude.Allow = append(merged.Claude.Allow, other.Claude.Allow...)
			}
		}
	}

	// [git]
	if other.Git != nil {
		if merged.Git == nil {
			merged.Git = &Git{}
		}
		if other.Git.Excludes != nil {
			if len(other.Git.Excludes) == 0 {
				merged.Git.Excludes = []string{}
			} else {
				merged.Git.Excludes = append(merged.Git.Excludes, other.Git.Excludes...)
			}
		}
	}

	// [direnv]
	if other.Direnv != nil {
		if merged.Direnv == nil {
			merged.Direnv = &Direnv{}
		}
		if other.Direnv.Envrc != nil {
			if len(other.Direnv.Envrc) == 0 {
				merged.Direnv.Envrc = []string{}
			} else {
				merged.Direnv.Envrc = append(merged.Direnv.Envrc, other.Direnv.Envrc...)
			}
		}
		if other.Direnv.Dotenv != nil {
			if merged.Direnv.Dotenv == nil {
				merged.Direnv.Dotenv = make(map[string]string)
			}
			for k, v := range other.Direnv.Dotenv {
				merged.Direnv.Dotenv[k] = v
			}
		}
	}

	// [hooks]
	if other.Hooks != nil {
		if merged.Hooks == nil {
			merged.Hooks = &Hooks{}
		}
		if other.Hooks.Create != nil {
			merged.Hooks.Create = other.Hooks.Create
		}
		if other.Hooks.Stop != nil {
			merged.Hooks.Stop = other.Hooks.Stop
		}
		if other.Hooks.PreMerge != nil {
			merged.Hooks.PreMerge = other.Hooks.PreMerge
		}
		if other.Hooks.OnAttach != nil {
			merged.Hooks.OnAttach = other.Hooks.OnAttach
		}
		if other.Hooks.OnDetach != nil {
			merged.Hooks.OnDetach = other.Hooks.OnDetach
		}
		if other.Hooks.DisallowMainWorktree != nil {
			merged.Hooks.DisallowMainWorktree = other.Hooks.DisallowMainWorktree
		}
		if other.Hooks.ToolUseLog != nil {
			merged.Hooks.ToolUseLog = other.Hooks.ToolUseLog
		}
		if other.Hooks.DisableMerge != nil {
			merged.Hooks.DisableMerge = other.Hooks.DisableMerge
		}
		if other.Hooks.DisableNixGC != nil {
			merged.Hooks.DisableNixGC = other.Hooks.DisableNixGC
		}
		if other.Hooks.PreMergeOutputFormat != nil {
			merged.Hooks.PreMergeOutputFormat = other.Hooks.PreMergeOutputFormat
		}
	}

	// [session-entry]
	if other.SessionEntry != nil {
		if merged.SessionEntry == nil {
			merged.SessionEntry = &SessionEntry{}
		}
		if len(other.SessionEntry.Start) > 0 {
			merged.SessionEntry.Start = other.SessionEntry.Start
		}
		if len(other.SessionEntry.Resume) > 0 {
			merged.SessionEntry.Resume = other.SessionEntry.Resume
		}
		// Env: per-key merge so a child sweatfile can add SPINCLASS_FOO=...
		// without dropping the parent's SPINCLASS_GROUP=...; child entries
		// override parent entries with the same key.
		if len(other.SessionEntry.Env) > 0 {
			if merged.SessionEntry.Env == nil {
				merged.SessionEntry.Env = make(map[string]string, len(other.SessionEntry.Env))
			}
			for k, v := range other.SessionEntry.Env {
				merged.SessionEntry.Env[k] = v
			}
		}
		if len(other.SessionEntry.LivenessProbe) > 0 {
			merged.SessionEntry.LivenessProbe = other.SessionEntry.LivenessProbe
		}
		if other.SessionEntry.TombstoneRetention != "" {
			merged.SessionEntry.TombstoneRetention = other.SessionEntry.TombstoneRetention
		}
	}

	// [[start-commands]] — append across levels, then dedupe by Name with
	// the most-specific (last) definition winning. Entries within a single
	// level preserve their relative order; cross-level overrides keep the
	// position of the first occurrence so iteration order stays stable.
	if len(other.StartCommands) > 0 {
		cp := make([]StartCommand, len(merged.StartCommands))
		copy(cp, merged.StartCommands)
		merged.StartCommands = cp
		index := make(map[string]int, len(merged.StartCommands))
		for i, sc := range merged.StartCommands {
			index[sc.Name] = i
		}
		for _, sc := range other.StartCommands {
			if i, ok := index[sc.Name]; ok {
				merged.StartCommands[i] = sc
				continue
			}
			index[sc.Name] = len(merged.StartCommands)
			merged.StartCommands = append(merged.StartCommands, sc)
		}
	}

	// allowed-mcps: nil=inherit, empty=clear, non-empty=append
	if other.AllowedMCPs != nil {
		if len(other.AllowedMCPs) == 0 {
			merged.AllowedMCPs = []string{}
		} else {
			merged.AllowedMCPs = append(merged.AllowedMCPs, other.AllowedMCPs...)
		}
	}

	// [[mcps]] — dedup-by-name, same pattern as [[start-commands]]
	if len(other.MCPs) > 0 {
		cp := make([]MCPServerDef, len(merged.MCPs))
		copy(cp, merged.MCPs)
		merged.MCPs = cp
		index := make(map[string]int, len(merged.MCPs))
		for i, mcp := range merged.MCPs {
			index[mcp.Name] = i
		}
		for _, mcp := range other.MCPs {
			if i, ok := index[mcp.Name]; ok {
				merged.MCPs[i] = mcp
				continue
			}
			index[mcp.Name] = len(merged.MCPs)
			merged.MCPs = append(merged.MCPs, mcp)
		}
	}

	// [[pre-merge-skills]] — dedup-by-name, same pattern as [[mcps]].
	// A name-only entry (empty rationale) is preserved here so that
	// ActivePreMergeSkills() can filter it out as a removal sentinel
	// against the inherited list.
	if len(other.PreMergeSkills) > 0 {
		cp := make([]PreMergeSkill, len(merged.PreMergeSkills))
		copy(cp, merged.PreMergeSkills)
		merged.PreMergeSkills = cp
		index := make(map[string]int, len(merged.PreMergeSkills))
		for i, s := range merged.PreMergeSkills {
			index[s.Name] = i
		}
		for _, s := range other.PreMergeSkills {
			if i, ok := index[s.Name]; ok {
				merged.PreMergeSkills[i] = s
				continue
			}
			index[s.Name] = len(merged.PreMergeSkills)
			merged.PreMergeSkills = append(merged.PreMergeSkills, s)
		}
	}

	return merged
}
