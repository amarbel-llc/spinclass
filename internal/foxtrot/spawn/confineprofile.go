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

// checkConfinedToolSurface is R3: the profile must leave no moxy grit_* tool
// reachable and keep just-us.
func checkConfinedToolSurface(held string, rp ResolvedProfile) error {
	if rp.KeptServers == nil {
		return fmt.Errorf("spawn refused for a confined worker: %s keeps every server (no saved selection), so moxy grit_* tools stay reachable; "+
			"use a selective profile that drops %q and keeps %q", held, moxyServer, justUsServer)
	}
	kept := strings.Join(rp.KeptServers, ", ")
	if slices.Contains(rp.KeptServers, moxyServer) {
		return fmt.Errorf("spawn refused for a confined worker: %s keeps the %q server (kept: %s), so moxy grit_* tools stay reachable even with per-tool exclusions; "+
			"use a profile that drops %q whole and keeps %q", held, moxyServer, kept, moxyServer, justUsServer)
	}
	if !slices.Contains(rp.KeptServers, justUsServer) {
		return fmt.Errorf("spawn refused for a confined worker: %s drops the %q server (kept: %s), so the worker has no run_recipe; "+
			"use a profile that drops %q whole and keeps %q", held, justUsServer, kept, moxyServer, justUsServer)
	}
	return nil
}
