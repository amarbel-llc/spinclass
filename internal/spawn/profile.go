package spawn

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// ErrProfileNotFound is the sentinel a ProfileResolver wraps (see
// ProfileNotFound) when the named clown profile does not exist.
var ErrProfileNotFound = errors.New("clown profile not found")

// ProfileNotFound wraps ErrProfileNotFound with the requested name, so a
// refusal names the missing thing (FDR 0034 "Fail closed").
func ProfileNotFound(name string) error {
	return fmt.Errorf("%w: %q", ErrProfileNotFound, name)
}

// ResolvedProfile is what spinclass needs from clown's profile library
// (FDR 0034 "The clown library"): the profile's name, the content digest of its
// resolved definition, and the MCP servers it keeps. A nil KeptServers means
// the profile makes no cheap-context selection, i.e. keeps everything; a
// non-nil empty one means a selection that kept nothing.
type ResolvedProfile struct {
	Name        string
	Digest      string
	KeptServers []string
}

// ProfileResolver looks a profile name up through clown's library. It is an
// interface so tests can inject a fake instead of needing a real registry.
type ProfileResolver interface {
	Resolve(name string) (ResolvedProfile, error)
}

// profileResolver is the resolver LaunchDetached consults. Swapped by tests;
// production resolves through clown's profilelookup library, which refuses an
// unknown profile or an unvouched registry rather than guessing.
var profileResolver ProfileResolver = newClownResolver()

// ProfileAuthorization is a granted profile right: the digest of the grant
// record that proved it, and the step that gives the worker its own (narrower)
// right once its session key is known.
type ProfileAuthorization struct {
	GrantDigest string
	IssueChild  func(childSession string) error
	// Undo, best-effort and possibly nil, unbinds whatever IssueChild minted. A
	// spawn calls it when a step after IssueChild fails.
	Undo func()
}

// ProfileAuthorizer proves the driver holds the resolved profile (FDR 0034
// Phase 2). It is an interface so tests can inject a fake instead of building
// signed grant chains.
type ProfileAuthorizer interface {
	Authorize(driver Driver, profile ResolvedProfile) (ProfileAuthorization, error)
}

// newProfileAuthorizer builds the authorizer a spawn consults when profile
// rights are enforced. It is only called then, so a default spawn never touches
// the grant store. Swapped by tests; production is confinement-gated and today
// always refuses (profilerights.Unestablished).
var newProfileAuthorizer = newProductionAuthorizer

// RequiredServersKept errors, naming the first missing server, when kept lacks
// any of required: a profile that drops one of those yields a worker that can
// never send its hello (FDR 0034 Limitations).
func RequiredServersKept(kept, required []string) error {
	for _, r := range required {
		if !slices.Contains(kept, r) {
			return fmt.Errorf("profile drops required server %q (kept: %s)", r, strings.Join(kept, ", "))
		}
	}
	return nil
}

// resolveProfile resolves name through profileResolver and applies the
// required-server check. A nil KeptServers skips the check: no cheap-context
// selection means every server is kept. A non-nil empty slice is a selection
// that kept nothing, and is checked (and refused) like any other.
func resolveProfile(name string) (ResolvedProfile, error) {
	rp, err := profileResolver.Resolve(name)
	if err != nil {
		return ResolvedProfile{}, err
	}
	if rp.Name == "" {
		rp.Name = name
	}
	if rp.KeptServers != nil {
		if err := RequiredServersKept(rp.KeptServers, profilelookup.RequiredServers()); err != nil {
			return ResolvedProfile{}, fmt.Errorf("profile %q: %w", name, err)
		}
	}
	return rp, nil
}

// ResolveProfile resolves name exactly as a spawn does (clown's library, the
// required-server check, the markl-id digest), for callers outside this
// package: resume, and the command that pins a profile into a grant.
func ResolveProfile(name string) (ResolvedProfile, error) { return resolveProfile(name) }

// SpliceProfileFlag places "--profile <profile>" among clown's own flags: just
// before the literal "--" provider-args separator, since clown flags must
// precede it. Any "--profile <x>" / "--profile=<x>" already before the
// separator is replaced. An empty profile returns entry unchanged; a requested
// profile with no "--" separator is a hard error (no silent fallback). Does not
// mutate entry.
func SpliceProfileFlag(entry []string, profile string) ([]string, error) {
	if profile == "" {
		return slices.Clone(entry), nil
	}
	idx := slices.Index(entry, "--")
	if idx == -1 {
		return nil, fmt.Errorf(
			"profile %q requested but this spawn-entry has no \"--\" provider-args separator to place --profile before: %v",
			profile, entry,
		)
	}
	out := make([]string, 0, len(entry)+2)
	for i := 0; i < idx; i++ {
		switch {
		case entry[i] == "--profile" || entry[i] == "-profile":
			i++ // drop the flag's value too
		case strings.HasPrefix(entry[i], "--profile="), strings.HasPrefix(entry[i], "-profile="):
		default:
			out = append(out, entry[i])
		}
	}
	out = append(out, "--profile", profile)
	out = append(out, entry[idx:]...)
	return out, nil
}
