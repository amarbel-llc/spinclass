package spawn

import (
	"fmt"
	"strings"
)

// KnownModelAliases is the fixed set of short model aliases accepted by the
// `model` param on spawn-session/fork-session (and sc spawn/sc fork
// --brief). Update as models are renamed or added — see the design doc's
// Tuning Levers (docs/plans/2026-07-11-spawn-model-selection-design.md).
var KnownModelAliases = []string{"sonnet", "opus", "haiku", "fable"}

// ValidateModelAlias returns an error unless alias is one of
// KnownModelAliases. Callers only invoke this when a model was actually
// requested (empty string means "no model requested" and is handled by the
// caller, not this function).
func ValidateModelAlias(alias string) error {
	for _, a := range KnownModelAliases {
		if alias == a {
			return nil
		}
	}
	return fmt.Errorf("unrecognized model %q (want one of: %s)", alias, strings.Join(KnownModelAliases, ", "))
}

// resolveProvider scans entry elements BEFORE the literal "--" separator for
// --provider <name> or --provider=<name> (clown's own flag grammar).
// Defaults to "claude" — clown's own default — when absent. A --provider
// occurring AFTER "--" is a provider-arg, not a clown flag, and is ignored.
func resolveProvider(entry []string) string {
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
	}
	return "claude"
}

// SpliceModelFlag inserts the resolved provider's model flag and alias into
// entry immediately after the literal "--" provider-args separator (before
// {prompt}/{dir} substitution — SubstituteEntry runs on the result). Does
// not mutate entry. Hard errors — no silent fallback — when entry has no
// "--" element, or the resolved provider has no entry in flags.
func SpliceModelFlag(entry []string, alias string, flags map[string]string) ([]string, error) {
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
	flag, ok := flags[provider]
	if !ok {
		return nil, fmt.Errorf(
			"model %q requested but provider %q has no [session-entry.model-flags] entry "+
				"(add e.g. model-flags.%s = \"--model\" to the sweatfile once the flag is verified)",
			alias, provider, provider,
		)
	}
	out := make([]string, 0, len(entry)+2)
	out = append(out, entry[:idx+1]...)
	out = append(out, flag, alias)
	out = append(out, entry[idx+1:]...)
	return out, nil
}
