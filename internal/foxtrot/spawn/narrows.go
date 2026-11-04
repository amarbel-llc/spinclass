package spawn

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/profilegrant"
)

// catalogWidenings lists every way child widens base at the catalog level
// (provider, backend, model, the kept-server set, per-tool exclusions, env). An
// empty result means child is a narrowing of base. Items are deterministic:
// fixed category order, sorted within a category.
func catalogWidenings(base, child ResolvedProfile) []string {
	var out []string
	if child.Provider != base.Provider {
		out = append(out, fmt.Sprintf("changes provider %s to %s", base.Provider, child.Provider))
	}
	if child.Backend != base.Backend {
		out = append(out, fmt.Sprintf("changes backend %s to %s", base.Backend, child.Backend))
	}
	// A base is declared, not inherited (clown), so a child with no model runs
	// clown's default rather than the base's: absent is a widening when the
	// base names a model (coordinator ruling, strict).
	switch {
	case child.Model == "" && base.Model != "":
		out = append(out, fmt.Sprintf("leaves the model unset (base names %s; clown would run its default)", base.Model))
	case child.Model != base.Model:
		out = append(out, fmt.Sprintf("changes model %s to %s", base.Model, child.Model))
	}
	out = append(out, serverWidenings(base, child)...)
	out = append(out, exclusionWidenings(base, child)...)
	out = append(out, envWidenings(base, child)...)
	out = append(out, moxyWidenings(base, child)...)
	return out
}

// moxyWidenings compares the moxy tables narrow-only (spinclass#457): a child
// may drop tools from the base's effective set, never add one, and may not
// change the placement or widen the ceiling (allow_tools). A table where the
// base has none is a widening; a child without a table under a base that has
// one is reported too, since the child then keeps moxy on R3's terms, not the
// base's.
func moxyWidenings(base, child ResolvedProfile) []string {
	switch {
	case base.Moxy == nil && child.Moxy == nil:
		return nil
	case base.Moxy == nil:
		return []string{fmt.Sprintf("adds a moxy table (placement %s; base has none)", child.Moxy.Placement)}
	case child.Moxy == nil:
		return []string{fmt.Sprintf("drops the moxy table (base places moxy %s)", base.Moxy.Placement)}
	}
	var out []string
	if child.Moxy.Placement != base.Moxy.Placement {
		out = append(out, fmt.Sprintf("changes moxy placement %s to %s", base.Moxy.Placement, child.Moxy.Placement))
	}
	for _, tool := range sortedCopy(child.Moxy.AllowTools) {
		if !slices.Contains(base.Moxy.AllowTools, tool) {
			out = append(out, "widens the moxy ceiling by "+tool)
		}
	}
	for _, tool := range sortedCopy(child.Moxy.Tools) {
		if !slices.Contains(base.Moxy.Tools, tool) {
			out = append(out, "adds moxy tool "+tool)
		}
	}
	return out
}

func serverWidenings(base, child ResolvedProfile) []string {
	if base.KeptServers == nil {
		return nil
	}
	if child.KeptServers == nil {
		return []string{fmt.Sprintf("keeps every server (base keeps only: %s)", strings.Join(base.KeptServers, ", "))}
	}
	var out []string
	for _, s := range sortedCopy(child.KeptServers) {
		if !slices.Contains(base.KeptServers, s) {
			out = append(out, "adds server "+s)
		}
	}
	return out
}

// exclusionWidenings reports each base per-tool exclusion the child lifts on a
// server the child keeps. A non-selective child keeps every server.
func exclusionWidenings(base, child ResolvedProfile) []string {
	var out []string
	for _, server := range slices.Sorted(maps.Keys(base.Excluded)) {
		if child.KeptServers != nil && !slices.Contains(child.KeptServers, server) {
			continue
		}
		for _, tool := range sortedCopy(base.Excluded[server]) {
			if !slices.Contains(child.Excluded[server], tool) {
				out = append(out, fmt.Sprintf("drops exclusion %s:%s", server, tool))
			}
		}
	}
	return out
}

func envWidenings(base, child ResolvedProfile) []string {
	if !base.EnvKnown || !child.EnvKnown {
		return []string{"env cannot be compared (the resolver reported no env digests)"}
	}
	var out []string
	for _, key := range slices.Sorted(maps.Keys(child.Env)) {
		baseDigest, inBase := base.Env[key]
		switch {
		case !inBase:
			out = append(out, "adds env key "+key)
		case baseDigest != child.Env[key]:
			out = append(out, "changes env value of "+key)
		}
	}
	return out
}

// baseChainVia is the D1 stacking input for grant verification
// (profilegrant.WalkOptions.BaseChain): profile@digest's _base chain, nearest
// base first, each link with the profile one step down's catalog widenings of
// it. The profile is resolved by name through resolve and must still be at
// digest, or nothing about it can be trusted (fail closed); its bases are the
// ones clown resolved WITH it, never looked up again by name.
func baseChainVia(resolve func(string) (ResolvedProfile, error)) func(name, digest string) ([]profilegrant.BaseLink, error) {
	return func(name, digest string) ([]profilegrant.BaseLink, error) {
		rp, err := resolve(name)
		if err != nil {
			return nil, err
		}
		if rp.Digest != digest {
			return nil, fmt.Errorf("its live definition is %s, not %s", rp.Digest, digest)
		}
		var chain []profilegrant.BaseLink
		for cur := rp; cur.Base != ""; cur = *cur.BaseProfile {
			if cur.BaseProfile == nil {
				return nil, fmt.Errorf("profile %s names base %q but the resolver did not report its definition", cur.Name, cur.Base)
			}
			chain = append(chain, profilegrant.BaseLink{Name: cur.Base, Digest: cur.BaseProfile.Digest, Widenings: catalogWidenings(*cur.BaseProfile, cur)})
		}
		return chain, nil
	}
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}
