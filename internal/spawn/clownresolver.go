package spawn

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"code.linenisgreat.com/clown/pkg/profilelookup"
	"code.linenisgreat.com/piggy/go/pkgs/markl"
)

// ProfileDigestPurpose is the markl-id purpose a resolved profile's digest is
// recorded under in session state. spinclass owns this purpose (clown FDR 0022
// decision 7); the hashed bytes are clown's canonical-form sha256. clown itself
// pins and compares that digest as the library's bare hex string, which the
// resolver carries verbatim as ResolvedProfile.DigestHex.
const ProfileDigestPurpose = "clown-profile-digest-v1"

// ProfileLaunchEnv is the profile pin spinclass hands a clown launch (clown FDR
// 0022, #407): CLOWN_PROFILE_DIGEST, the digest in clown's own hex, which clown
// requires the applied profile to match and then unsets; and, when the launch
// resumes a session, CLOWN_PROFILE_SESSION, the claude --resume id the approval
// is bound to. It returns nil when there is no profile (digestHex ""). Callers
// that hold a profile with no digest must refuse rather than call this.
// CLOWN_PROFILE is never set: the --profile flag names the profile, and clown
// keeps CLOWN_PROFILE in its environment, where every descendant would inherit
// it.
func ProfileLaunchEnv(digestHex, resumeID string) []string {
	if digestHex == "" {
		return nil
	}
	env := []string{"CLOWN_PROFILE_DIGEST=" + digestHex}
	if resumeID != "" {
		env = append(env, "CLOWN_PROFILE_SESSION="+resumeID)
	}
	return env
}

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
		var baseErr *profilelookup.BaseError
		switch {
		case errors.Is(err, profilelookup.ErrNotFound):
			return ResolvedProfile{}, ProfileNotFound(name)
		case errors.As(err, &skew):
			// The library's message already names both sides and the fix.
			return ResolvedProfile{}, fmt.Errorf("cannot spawn with profile %q: %s", name, skew.Error())
		case errors.As(err, &baseErr):
			// Likewise: it names the profile, the base, and the pinned and current digests.
			return ResolvedProfile{}, fmt.Errorf("cannot spawn with profile %q: %s", name, baseErr.Error())
		default:
			return ResolvedProfile{}, fmt.Errorf("resolving clown profile %q: %w", name, err)
		}
	}

	rp, err := resolvedProfileOf(res)
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

	return rp, nil
}

// resolvedProfileOf maps clown's Resolved onto a ResolvedProfile, recursing into
// the base clown resolved as a builtin. The required-server check is the caller's.
func resolvedProfileOf(res *profilelookup.Resolved) (ResolvedProfile, error) {
	// Never put res.Canonical in an error or log: it carries the url verbatim.
	// Only the digest leaves this function.
	digest, err := profileDigestMarklID(res.DigestSHA256)
	if err != nil {
		return ResolvedProfile{}, err
	}
	rp := ResolvedProfile{
		Name: res.Name, Digest: digest, DigestHex: res.DigestSHA256, Provider: res.Provider, Backend: res.Backend, Model: res.Model,
		KeptServers: keptServers(res.Selection), Excluded: res.Selection.Excluded, Source: string(res.Source),
		EnvKnown: true, Env: maps.Clone(res.Env),
	}
	if res.Base != nil {
		base, err := resolvedProfileOf(res.Base)
		if err != nil {
			return ResolvedProfile{}, fmt.Errorf("base %q: %w", res.Base.Name, err)
		}
		rp.Base = res.Base.Name
		rp.BaseProfile = &base
	}
	return rp, nil
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
