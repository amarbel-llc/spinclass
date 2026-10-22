package spawn

import (
	"fmt"
	"maps"
	"slices"
	"strings"
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
		return []string{"env cannot be compared (clown does not expose a secrets-safe env view yet)"}
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

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}
