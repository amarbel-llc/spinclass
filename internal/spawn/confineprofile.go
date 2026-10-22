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

// requireConfinedProfile picks, resolves and vets the profile a CONFINED spawn
// boots with (FDR 0035): the explicit parameter, else the spawn-profile default
// read from trusted layers only (trusted). Nothing is created or written; a
// refusal names the profile the caller holds and what would satisfy it.
func requireConfinedProfile(explicit, trusted string) (name string, rp ResolvedProfile, err error) {
	name = explicit
	if name == "" {
		name = trusted
	}
	if name == "" {
		return "", ResolvedProfile{}, errors.New("spawn refused: a confined worker requires a clown profile, and none was given " +
			"(no profile parameter, and no sweatfile above the repo sets [session-entry] spawn-profile); " +
			"pass profile, or set [session-entry] spawn-profile in a sweatfile above the repo " +
			"(the global sweatfile or a parent directory's; the repo's own sweatfile is not read for a confined spawn)")
	}
	rp, err = resolveProfile(name)
	if err != nil {
		return "", ResolvedProfile{}, err
	}
	if err := checkConfinedProfile(name, rp); err != nil {
		return "", ResolvedProfile{}, err
	}
	return name, rp, nil
}

// checkConfinedProfile is the provenance and tool-surface rule for a resolved
// profile of a confined spawn. Only one form is accepted today: a selective
// profile that drops the moxy server whole and keeps just-us (so run_recipe
// works), which leaves no moxy grit_* tool reachable.
func checkConfinedProfile(name string, rp ResolvedProfile) error {
	held := fmt.Sprintf("profile %q (digest %s, source %q)", name, rp.Digest, rp.Source)
	switch rp.Source {
	case "", profileSourceDynamic:
		return fmt.Errorf("spawn refused for a confined worker: %s has an unknown or dynamic source, "+
			"and dynamic or unknown-source profiles are refused for confined spawns until a verified grant names its digest",
			held)
	}
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
