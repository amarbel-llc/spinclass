package sweatfile

// LoadSource records one sweatfile in a resolved Hierarchy: where it was looked
// for, whether it existed, its parsed contents, and (when deduplicated away) the
// reason it was skipped. The hierarchy loaders live in internal/sweatfileio; the
// type stays here because it's the public shape callers consume.
type LoadSource struct {
	Path       string
	Found      bool
	File       Sweatfile
	SkipReason string
}

// Hierarchy is the merged result of the sweatfile cascade plus the per-level
// sources that produced it. Built by internal/sweatfileio's loaders.
type Hierarchy struct {
	Sources []LoadSource
	Merged  Sweatfile
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
		if other.Hooks.Repair != nil {
			merged.Hooks.Repair = other.Hooks.Repair
		}
		if other.Hooks.PreCommit != nil {
			merged.Hooks.PreCommit = other.Hooks.PreCommit
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
		if other.Hooks.DisableMergeQueue != nil {
			merged.Hooks.DisableMergeQueue = other.Hooks.DisableMergeQueue
		}
		if other.Hooks.DisableRepair != nil {
			merged.Hooks.DisableRepair = other.Hooks.DisableRepair
		}
		if other.Hooks.DisablePreCommit != nil {
			merged.Hooks.DisablePreCommit = other.Hooks.DisablePreCommit
		}
		if other.Hooks.DisableNixGC != nil {
			merged.Hooks.DisableNixGC = other.Hooks.DisableNixGC
		}
		if other.Hooks.DisableImplicitSessions != nil {
			merged.Hooks.DisableImplicitSessions = other.Hooks.DisableImplicitSessions
		}
		if other.Hooks.DisableMergeBuildWorktree != nil {
			merged.Hooks.DisableMergeBuildWorktree = other.Hooks.DisableMergeBuildWorktree
		}
		if other.Hooks.DisableWorktreePathRewrite != nil {
			merged.Hooks.DisableWorktreePathRewrite = other.Hooks.DisableWorktreePathRewrite
		}
		if other.Hooks.PreMergeOutputFormat != nil {
			merged.Hooks.PreMergeOutputFormat = other.Hooks.PreMergeOutputFormat
		}
		if other.Hooks.InactivityTimeout != nil {
			merged.Hooks.InactivityTimeout = other.Hooks.InactivityTimeout
		}
		if other.Hooks.AutoRebuildOnResume != nil {
			merged.Hooks.AutoRebuildOnResume = other.Hooks.AutoRebuildOnResume
		}
	}

	// [sysprompt]
	if other.Sysprompt != nil {
		if merged.Sysprompt == nil {
			merged.Sysprompt = &Sysprompt{}
		}
		// doc-index-dirs: OVERRIDE, not append. A non-nil value — including an
		// explicit [] — replaces the inherited list (override-wins down the
		// hierarchy); [] therefore disables the index. These are scan roots, so
		// override is the natural semantics, diverging from the append default
		// of other string arrays. nil inherits.
		if other.Sysprompt.DocIndexDirs != nil {
			merged.Sysprompt.DocIndexDirs = other.Sysprompt.DocIndexDirs
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
		// ModelFlags: per-key merge, same rationale as Env.
		if len(other.SessionEntry.ModelFlags) > 0 {
			if merged.SessionEntry.ModelFlags == nil {
				merged.SessionEntry.ModelFlags = make(map[string]string, len(other.SessionEntry.ModelFlags))
			}
			for k, v := range other.SessionEntry.ModelFlags {
				merged.SessionEntry.ModelFlags[k] = v
			}
		}
		if len(other.SessionEntry.LivenessProbe) > 0 {
			merged.SessionEntry.LivenessProbe = other.SessionEntry.LivenessProbe
		}
		if other.SessionEntry.TombstoneRetention != "" {
			merged.SessionEntry.TombstoneRetention = other.SessionEntry.TombstoneRetention
		}
		if len(other.SessionEntry.SpawnEntry) > 0 {
			merged.SessionEntry.SpawnEntry = other.SessionEntry.SpawnEntry
		}
		if len(other.SessionEntry.SpawnWindow) > 0 {
			merged.SessionEntry.SpawnWindow = other.SessionEntry.SpawnWindow
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

	// [[remotes]] — dedup-by-name, same merge mechanics as [[mcps]], but
	// removal is explicit: a `remove = true` entry overrides the inherited
	// remote in place and is preserved here so that ActiveRemotes() can
	// filter it out. Name-only entries are NOT sentinels — they declare
	// all-defaults remotes.
	if len(other.Remotes) > 0 {
		cp := make([]Remote, len(merged.Remotes))
		copy(cp, merged.Remotes)
		merged.Remotes = cp
		index := make(map[string]int, len(merged.Remotes))
		for i, r := range merged.Remotes {
			index[r.Name] = i
		}
		for _, r := range other.Remotes {
			if i, ok := index[r.Name]; ok {
				merged.Remotes[i] = r
				continue
			}
			index[r.Name] = len(merged.Remotes)
			merged.Remotes = append(merged.Remotes, r)
		}
	}

	return merged
}
