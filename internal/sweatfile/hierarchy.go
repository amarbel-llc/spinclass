package sweatfile

import (
	"os"
	"path/filepath"
	"strings"
)

type LoadSource struct {
	Path  string
	Found bool
	File  Sweatfile
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

func LoadHierarchy(home, repoDir string) (Hierarchy, error) {
	var sources []LoadSource
	merged := Sweatfile{}

	loadAndMerge := func(path string) error {
		doc, err := Load(path)
		if err != nil {
			return err
		}
		sf := *doc.Data()
		_, found := fileExists(path)
		sources = append(
			sources,
			LoadSource{Path: path, Found: found, File: sf},
		)
		if found {
			merged = merged.MergeWith(sf)
		}
		return nil
	}

	// 1. Global config
	globalPath := filepath.Join(home, ".config", "spinclass", "sweatfile")
	if err := loadAndMerge(globalPath); err != nil {
		return Hierarchy{}, err
	}

	// 2. Parent directories walking DOWN from home to repo dir
	cleanHome := filepath.Clean(home)
	cleanRepo := filepath.Clean(repoDir)

	rel, err := filepath.Rel(cleanHome, cleanRepo)
	if err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
		parts := strings.Split(rel, string(filepath.Separator))
		// Walk each intermediate directory (excluding repo dir itself)
		for i := 1; i < len(parts); i++ {
			parentDir := filepath.Join(cleanHome, filepath.Join(parts[:i]...))
			parentPath := filepath.Join(parentDir, "sweatfile")
			if err := loadAndMerge(parentPath); err != nil {
				return Hierarchy{}, err
			}
		}
	}

	// 3. Repo sweatfile
	repoPath := filepath.Join(cleanRepo, "sweatfile")
	if err := loadAndMerge(repoPath); err != nil {
		return Hierarchy{}, err
	}

	return Hierarchy{
		Sources: sources,
		Merged:  merged,
	}, nil
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
		if other.Claude.SystemPrompt != nil {
			if *other.Claude.SystemPrompt == "" {
				merged.Claude.SystemPrompt = other.Claude.SystemPrompt
			} else if merged.Claude.SystemPrompt != nil && *merged.Claude.SystemPrompt != "" {
				joined := *merged.Claude.SystemPrompt + " " + *other.Claude.SystemPrompt
				merged.Claude.SystemPrompt = &joined
			} else {
				merged.Claude.SystemPrompt = other.Claude.SystemPrompt
			}
		}
		if other.Claude.SystemPromptAppend != nil {
			if *other.Claude.SystemPromptAppend == "" {
				merged.Claude.SystemPromptAppend = other.Claude.SystemPromptAppend
			} else if merged.Claude.SystemPromptAppend != nil && *merged.Claude.SystemPromptAppend != "" {
				joined := *merged.Claude.SystemPromptAppend + " " + *other.Claude.SystemPromptAppend
				merged.Claude.SystemPromptAppend = &joined
			} else {
				merged.Claude.SystemPromptAppend = other.Claude.SystemPromptAppend
			}
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
		if other.Hooks.DisallowMainWorktree != nil {
			merged.Hooks.DisallowMainWorktree = other.Hooks.DisallowMainWorktree
		}
		if other.Hooks.ToolUseLog != nil {
			merged.Hooks.ToolUseLog = other.Hooks.ToolUseLog
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
	}

	return merged
}
