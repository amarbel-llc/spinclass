package spawn

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"code.linenisgreat.com/clown/pkg/profilelookup"
	"code.linenisgreat.com/piggy/go/pkgs/markl"
)

// ProfileDigestPurpose is the markl-id purpose a resolved profile's digest is
// rendered under. Unregistered purposes are legal and carried opaquely by
// markl; clown should own or confirm this name, since the digest is clown's
// canonical-form hash.
const ProfileDigestPurpose = "clown-profile-digest-v1"

// clownResolver resolves profiles in-process through clown's profilelookup
// library (clown#264, FDR 0034). lookup is a seam so tests need no registry.
type clownResolver struct {
	lookup func(name string, opts profilelookup.Options) (*profilelookup.Resolved, error)
	// registryPath overrides the registry location; "" uses the library default.
	registryPath string
}

func newClownResolver() clownResolver {
	return clownResolver{lookup: profilelookup.Resolve}
}

func (r clownResolver) Resolve(name string) (ResolvedProfile, error) {
	res, err := r.lookup(name, profilelookup.Options{RegistryPath: r.registryPath})
	if err != nil {
		var skew *profilelookup.SkewError
		switch {
		case errors.Is(err, profilelookup.ErrNotFound):
			return ResolvedProfile{}, ProfileNotFound(name)
		case errors.As(err, &skew):
			// The library's message already names both sides and the fix.
			return ResolvedProfile{}, fmt.Errorf("cannot spawn with profile %q: %s", name, skew.Error())
		default:
			return ResolvedProfile{}, fmt.Errorf("resolving clown profile %q: %w", name, err)
		}
	}

	// Never put res.Canonical in an error or log: it carries env values and
	// the url verbatim. Only the digest leaves this function.
	digest, err := profileDigestMarklID(res.DigestSHA256)
	if err != nil {
		return ResolvedProfile{}, fmt.Errorf("clown profile %q: %w", name, err)
	}

	if crippled := res.Selection.CrippledTools(profilelookup.RequiredServers()); len(crippled) > 0 {
		servers := make([]string, 0, len(crippled))
		for server := range crippled {
			servers = append(servers, server)
		}
		slices.Sort(servers)
		return ResolvedProfile{}, fmt.Errorf(
			"profile %q keeps required server %q but excludes some of its tools; the worker could not send its hello",
			name, servers[0],
		)
	}

	return ResolvedProfile{Name: res.Name, Digest: digest, Provider: res.Provider, KeptServers: keptServers(res.Selection), Source: string(res.Source)}, nil
}

// keptServers maps a Selection onto ResolvedProfile.KeptServers: nil when the
// profile saves no selection (keeps everything, nothing to check), and a
// non-nil slice, even an empty one, for a selection, because resolveProfile
// only runs the required-server check on a non-nil slice.
func keptServers(sel profilelookup.Selection) []string {
	if !sel.Selective {
		return nil
	}
	return append([]string{}, sel.Kept...)
}

// profileDigestMarklID renders the hex sha256 clown reports as a markl-id,
// "clown-profile-digest-v1@sha256-<blech32>".
func profileDigestMarklID(sha256Hex string) (string, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(sha256Hex))
	if err != nil {
		return "", fmt.Errorf("profile digest %q is not hex: %w", sha256Hex, err)
	}
	if len(raw) != sha256.Size {
		return "", fmt.Errorf("profile digest is %d bytes, want %d", len(raw), sha256.Size)
	}
	var id markl.Id
	if err := id.SetPurposeId(ProfileDigestPurpose); err != nil {
		return "", err
	}
	if err := id.SetMarklId(markl.FormatIdHashSha256, raw); err != nil {
		return "", fmt.Errorf("profile digest: %w", err)
	}
	text, err := id.MarshalText()
	if err != nil {
		return "", fmt.Errorf("profile digest: %w", err)
	}
	return string(text), nil
}
