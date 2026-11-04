package spawn

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Server names in a profile's keep-set, in clown's "<plugin>/<server>"
// vocabulary (the same one profilelookup.RequiredServers uses).
const (
	moxyServer   = "moxy/moxy"
	justUsServer = "just-us/just-us"
)

// profileSourceDynamic is the Source of a profile minted at run time rather than
// read from a registry. Clown's library does not report it today; the check is
// ready for the release that does.
const profileSourceDynamic = "dynamic"

// Sources clown's library reports for a registry-backed profile.
const (
	profileSourceBuiltin = "builtin"
	profileSourceUser    = "user"
)

// requireConfinedProfile picks, resolves and vets the profile a CONFINED spawn
// boots with (FDR 0035): the explicit parameter, else the spawn-profile default
// read from trusted layers only (trusted). Nothing is created or written; a
// refusal names the profile the caller holds and what would satisfy it.
//
// narrowing is non-nil only when a user profile was accepted as a narrowing of
// its base builtin; it names that base for the worker's recorded link.
func requireConfinedProfile(explicit, trusted string) (name string, rp ResolvedProfile, narrowing *narrowingBase, err error) {
	name = explicit
	if name == "" {
		name = trusted
	}
	if name == "" {
		return "", ResolvedProfile{}, nil, errors.New("spawn refused: a confined worker requires a clown profile, and none was given " +
			"(no profile parameter, and no sweatfile above the repo sets [session-entry] spawn-profile); " +
			"pass profile, or set [session-entry] spawn-profile in a sweatfile above the repo " +
			"(the global sweatfile or a parent directory's; the repo's own sweatfile is not read for a confined spawn)")
	}
	rp, err = resolveProfile(name)
	if err != nil {
		return "", ResolvedProfile{}, nil, err
	}
	narrowing, err = checkConfinedProfile(name, rp)
	if err != nil {
		return "", ResolvedProfile{}, nil, err
	}
	return name, rp, narrowing, nil
}

// narrowingBase names the builtin a user profile was accepted as narrowing, and
// that builtin's digest.
type narrowingBase struct {
	Name   string
	Digest string
}

// checkConfinedProfile is the provenance and tool-surface rule for a resolved
// profile of a confined spawn (FDR 0035 R3, R4). Only one tool-surface form is
// accepted: a selective profile that drops the moxy server whole and keeps
// just-us (so run_recipe works), which leaves no moxy grit_* tool reachable.
// A builtin passes R4 as is; a user-registry profile passes only as a narrowing
// of the builtin it names as its base (Tier 1), and the base is returned.
func checkConfinedProfile(name string, rp ResolvedProfile) (*narrowingBase, error) {
	held := fmt.Sprintf("profile %q (digest %s, source %q)", name, rp.Digest, rp.Source)
	var narrowing *narrowingBase
	switch rp.Source {
	case "", profileSourceDynamic:
		return nil, fmt.Errorf("spawn refused for a confined worker: %s has an unknown or dynamic source, "+
			"and dynamic or unknown-source profiles are refused for confined spawns until a verified grant names its digest",
			held)
	case profileSourceUser:
		var err error
		if narrowing, err = checkUserNarrowing(name, held, rp); err != nil {
			return nil, err
		}
	}
	if err := checkConfinedToolSurface(held, rp); err != nil {
		return nil, err
	}
	return narrowing, nil
}

// checkUserNarrowing accepts a user-registry profile only when it declares a
// base builtin and catalogWidenings finds nothing the child adds to it. The base
// is the one clown resolved with the profile (rp.BaseProfile), never looked up
// again by name: a user profile may shadow a builtin's name.
func checkUserNarrowing(name, held string, rp ResolvedProfile) (*narrowingBase, error) {
	if rp.Base == "" {
		return nil, fmt.Errorf("spawn refused for a confined worker: %s declares no base builtin, "+
			"so it cannot be checked as a narrowing of one; a user profile is accepted for a confined spawn only when it narrows "+
			"a builtin it names as its base", held)
	}
	if rp.BaseProfile == nil {
		return nil, fmt.Errorf("spawn refused for a confined worker: %s names base %q, but the resolver did not report the base's resolved definition, "+
			"so the narrowing cannot be checked", held, rp.Base)
	}
	base := *rp.BaseProfile
	if base.Source != profileSourceBuiltin {
		return nil, fmt.Errorf("spawn refused for a confined worker: %s names base %q whose source is %q, and only a builtin can be a base",
			held, rp.Base, base.Source)
	}
	if widenings := catalogWidenings(base, rp); len(widenings) > 0 {
		return nil, fmt.Errorf("spawn refused for a confined worker: profile %q widens base %q by: %s",
			name, rp.Base, strings.Join(widenings, "; "))
	}
	return &narrowingBase{Name: rp.Base, Digest: base.Digest}, nil
}

// checkConfinedToolSurface is R3 and R3': the profile must leave no moxy grit_*
// tool reachable and keep just-us. Moxy may be kept only through a moxy table
// (clown profile protocol 3) that places it in the unit under the ratified
// ceiling (checkInUnitMoxy, spinclass#457); without a table, R3's "dropped
// whole" is the only accepted form.
func checkConfinedToolSurface(held string, rp ResolvedProfile) error {
	if rp.KeptServers == nil {
		return fmt.Errorf("spawn refused for a confined worker: %s keeps every server (no saved selection), so moxy grit_* tools stay reachable; "+
			"use a selective profile that drops %q and keeps %q", held, moxyServer, justUsServer)
	}
	kept := strings.Join(rp.KeptServers, ", ")
	if slices.Contains(rp.KeptServers, moxyServer) {
		if rp.Moxy == nil {
			return fmt.Errorf("spawn refused for a confined worker: %s keeps the %q server (kept: %s) with no moxy table, so moxy grit_* tools stay reachable even with per-tool exclusions; "+
				"use a profile that drops %q whole and keeps %q, or one whose moxy table places moxy in-unit under the ratified tool ceiling (clown profile protocol 3)",
				held, moxyServer, kept, moxyServer, justUsServer)
		}
		if err := checkInUnitMoxy(held, *rp.Moxy); err != nil {
			return err
		}
	}
	if !slices.Contains(rp.KeptServers, justUsServer) {
		return fmt.Errorf("spawn refused for a confined worker: %s drops the %q server (kept: %s), so the worker has no run_recipe; "+
			"use a profile that drops %q whole and keeps %q", held, justUsServer, kept, moxyServer, justUsServer)
	}
	return nil
}

// Placements a profile's moxy table may name (clown profile protocol 3).
const (
	moxyPlacementInUnit   = "in-unit"
	moxyPlacementFrontend = "frontend"
	moxyPlacementTwin     = "twin"
)

// ratifiedMoxyTools is the V3 default ceiling the operator ratified on
// 2026-11-03 (clown agentscope.DefaultCeiling), in spinclass's own words: the
// moxins allowed whole, and the moxins allowed for the named tools only. A
// tool of any other moxin, or moxy's own tools (restart, batch, async and its
// kin: process-spawning or moxyfile-reloading), is outside it.
var ratifiedMoxyTools = struct {
	whole []string
	only  map[string][]string
}{
	whole: []string{"toolbox", "env", "man"},
	only: map[string][]string{
		"rg":    {"search"},
		"folio": {"read", "ls", "glob", "tree", "file-type", "mktemp", "write", "mkdir", "mv", "rm"},
	},
}

// checkInUnitMoxy is R3' (spinclass#457): a kept moxy is accepted only when
// the table places it in the unit, names at least one tool, keeps every tool
// under its own ceiling, and names nothing outside the ratified default: no
// grit_* (moxy's own git, the worker's git is spinclass's), no smith_*, none
// of moxy's own process-spawning or reloading tools, no moxin beyond the
// default. frontend placement with moxy kept stays R3's refusal; twin is
// refused until specified. Each refusal names the offending tool.
func checkInUnitMoxy(held string, m MoxyTable) error {
	switch m.Placement {
	case moxyPlacementInUnit:
	case moxyPlacementFrontend:
		return fmt.Errorf("spawn refused for a confined worker: %s keeps the %q server with its moxy table placing moxy in the frontend, so moxy grit_* tools stay reachable; "+
			"place it in-unit, or drop %q whole", held, moxyServer, moxyServer)
	case moxyPlacementTwin:
		return fmt.Errorf("spawn refused for a confined worker: %s places moxy as a twin unit, which is not specified yet; place it in-unit, or drop %q whole", held, moxyServer)
	default:
		return fmt.Errorf("spawn refused for a confined worker: %s names moxy placement %q, which this spinclass does not know (in-unit is the accepted one)", held, m.Placement)
	}
	if len(m.Tools) == 0 {
		return fmt.Errorf("spawn refused for a confined worker: %s places moxy in-unit but its moxy table names no tools, so the worker would carry a moxy with nothing to call; "+
			"name the tools, or drop %q whole", held, moxyServer)
	}
	for _, tool := range m.Tools {
		if !slices.Contains(m.AllowTools, tool) {
			return fmt.Errorf("spawn refused for a confined worker: %s names moxy tool %s outside its own ceiling (allow_tools); the table must hold tools ⊆ allow_tools", held, tool)
		}
		if reason := outsideRatifiedMoxy(tool); reason != "" {
			return fmt.Errorf("spawn refused for a confined worker: %s names moxy tool %s, %s; the ratified in-unit default is toolbox, env and man whole, rg search, and folio read, ls, glob, tree, file-type, mktemp, write, mkdir, mv and rm",
				held, tool, reason)
		}
	}
	return nil
}

// outsideRatifiedMoxy says why a moxy tool name (mcp__<server>__<tool>, the
// tool part being <moxin>_<name>, or a bare name for moxy's own tools) is not
// in the ratified default, or "" when it is. The server key is clown's to
// render; only the tool part is judged.
func outsideRatifiedMoxy(name string) string {
	rest, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return "which is not an mcp__<server>__<tool> name"
	}
	_, tool, ok := strings.Cut(rest, "__")
	if !ok || tool == "" {
		return "which is not an mcp__<server>__<tool> name"
	}
	moxin, sub, hasMoxin := strings.Cut(tool, "_")
	switch {
	case !hasMoxin:
		return "which is one of moxy's own tools (restart, batch, async and their kin: process-spawning or moxyfile-reloading), outside the ratified default"
	case moxin == "grit":
		return "moxy's own git; a confined worker's git is spinclass's grit_* tools (FDR 0036), and moxy's grit must not be reachable from the unit"
	case moxin == "smith":
		return "a forge-writing tool a confined worker files through issue-filer instead"
	case slices.Contains(ratifiedMoxyTools.whole, moxin):
		return ""
	}
	if allowed, ok := ratifiedMoxyTools.only[moxin]; ok {
		if slices.Contains(allowed, sub) {
			return ""
		}
		return fmt.Sprintf("a %s tool outside the ratified subset (%s)", moxin, strings.Join(allowed, ", "))
	}
	return fmt.Sprintf("a tool of moxin %q, which is not in the ratified default", moxin)
}
