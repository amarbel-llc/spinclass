package spawn

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateModelAlias returns an error if provider is "claude" and alias is
// not a key of modelIDs (the configured [session-entry.model-ids] map, built
// in for sonnet/opus/haiku/fable via sweatfile.GetDefault()). For any other
// (or profile-derived) provider it always returns nil: modelIDs is a
// Claude-alias-to-full-ID map, meaningless for providers with their own
// model namespace (juggler's GGUF filenames or registered gateway names, a
// future provider's own scheme, ...) that spinclass has no registry to
// validate against. Callers only invoke this when a model was actually
// requested (empty string means "no model requested" and is handled by the
// caller, not this function).
func ValidateModelAlias(alias, provider string, modelIDs map[string]string) error {
	if provider != "claude" {
		return nil
	}
	if _, ok := modelIDs[alias]; ok {
		return nil
	}
	known := make([]string, 0, len(modelIDs))
	for a := range modelIDs {
		known = append(known, a)
	}
	sort.Strings(known)
	return fmt.Errorf("unrecognized model %q (want one of: %s)", alias, strings.Join(known, ", "))
}

// resolveProvider scans entry elements BEFORE the literal "--" separator for
// --provider <name>/--provider=<name> or --profile <name>/--profile=<name>
// (clown's own flag grammar) and returns the value found. An explicit
// --provider wins over --profile if both are present, matching clown's own
// documented precedence (clownfile(5) [profile].profile: "an explicit
// --provider suppresses the pin ... but never an explicit --profile").
// --profile selects a provider indirectly through clown's named-profile
// registry, which spinclass has no way to resolve to a provider name from
// argv text alone — so the profile name itself is returned as the lookup
// key, and [session-entry.model-flags] entries may be keyed by profile name
// as well as provider name. Defaults to "claude" — clown's own default —
// when neither is present. A --provider/--profile occurring AFTER "--" is a
// provider-arg, not a clown flag, and is ignored.
func resolveProvider(entry []string) string {
	var profile string
	for i, e := range entry {
		if e == "--" {
			break
		}
		if e == "--provider" && i+1 < len(entry) {
			return entry[i+1]
		}
		if v, ok := strings.CutPrefix(e, "--provider="); ok {
			return v
		}
		if profile == "" && e == "--profile" && i+1 < len(entry) {
			profile = entry[i+1]
		}
		if profile == "" {
			if v, ok := strings.CutPrefix(e, "--profile="); ok {
				profile = v
			}
		}
	}
	if profile != "" {
		return profile
	}
	return "claude"
}

// SpliceModelFlag validates alias against the resolved provider (see
// ValidateModelAlias) and, if valid, inserts the provider's model flag and
// resolved model value into entry immediately after the literal "--"
// provider-args separator (before {prompt}/{dir} substitution —
// SubstituteEntry runs on the result). For the "claude" provider the
// resolved value is modelIDs[alias] (the full Claude model ID, e.g.
// "claude-opus-5") rather than the raw alias; every other provider gets the
// raw alias verbatim, since modelIDs is a Claude-only alias->ID map. Does
// not mutate entry. Hard errors — no silent fallback — when entry has no
// "--" element, the alias is invalid for the resolved provider, or the
// resolved provider has no entry in flags.
func SpliceModelFlag(entry []string, alias string, flags, modelIDs map[string]string) ([]string, error) {
	idx := -1
	for i, e := range entry {
		if e == "--" {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, fmt.Errorf(
			"model %q requested but this spawn-entry has no \"--\" provider-args separator to splice into: %v",
			alias, entry,
		)
	}
	provider := resolveProvider(entry)
	if err := ValidateModelAlias(alias, provider, modelIDs); err != nil {
		return nil, err
	}
	flag, ok := flags[provider]
	if !ok {
		return nil, fmt.Errorf(
			"model %q requested but provider %q has no [session-entry.model-flags] entry "+
				"(add e.g. model-flags.%s = \"--model\" to the sweatfile once the flag is verified)",
			alias, provider, provider,
		)
	}
	resolved := alias
	if provider == "claude" {
		resolved = modelIDs[alias]
	}
	out := make([]string, 0, len(entry)+2)
	out = append(out, entry[:idx+1]...)
	out = append(out, flag, resolved)
	out = append(out, entry[idx+1:]...)
	return out, nil
}
