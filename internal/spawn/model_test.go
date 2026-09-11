package spawn

import (
	"strings"
	"testing"
)

// testClaudeModelIDs mirrors sweatfile.GetDefault()'s built-in
// [session-entry.model-ids] seed, kept as a local fixture so this package's
// tests don't need to import internal/sweatfile just to exercise the
// alias->ID rewrite.
var testClaudeModelIDs = map[string]string{
	"sonnet": "claude-sonnet-5",
	"opus":   "claude-opus-5",
	"haiku":  "claude-haiku-4-5-20251001",
	"fable":  "claude-fable-5-1",
}

func TestValidateModelAliasKnown(t *testing.T) {
	for alias := range testClaudeModelIDs {
		if err := ValidateModelAlias(alias, "claude", testClaudeModelIDs); err != nil {
			t.Errorf("ValidateModelAlias(%q, \"claude\", ...) = %v, want nil", alias, err)
		}
	}
}

func TestValidateModelAliasUnknown(t *testing.T) {
	if err := ValidateModelAlias("gpt5", "claude", testClaudeModelIDs); err == nil {
		t.Error("ValidateModelAlias(\"gpt5\", \"claude\", ...) = nil, want error")
	}
}

// TestValidateModelAliasPassesThroughForNonClaudeProvider pins the
// provider-conditional behavior: the configured modelIDs map is only
// meaningful for the claude provider. juggler/clownbox/anything else has
// its own model namespace (GGUF filenames, registered gateway names, ...)
// that spinclass doesn't know how to validate, so it must not reject them.
func TestValidateModelAliasPassesThroughForNonClaudeProvider(t *testing.T) {
	for _, provider := range []string{"juggler", "clownbox", "codex", "some-future-provider"} {
		if err := ValidateModelAlias("totally-not-a-claude-alias", provider, testClaudeModelIDs); err != nil {
			t.Errorf("ValidateModelAlias(%q, %q, ...) = %v, want nil (non-claude provider, unvalidated)", "totally-not-a-claude-alias", provider, err)
		}
	}
}

func TestResolveProviderDefault(t *testing.T) {
	got := resolveProvider([]string{"clown", "--clown-attach=spawn", "--", "{prompt}"})
	if got != "claude" {
		t.Errorf("resolveProvider() = %q, want \"claude\" (default)", got)
	}
}

func TestResolveProviderExplicitSpaceForm(t *testing.T) {
	got := resolveProvider([]string{"clown", "--provider", "codex", "--", "{prompt}"})
	if got != "codex" {
		t.Errorf("resolveProvider() = %q, want \"codex\"", got)
	}
}

func TestResolveProviderExplicitEqualsForm(t *testing.T) {
	got := resolveProvider([]string{"clown", "--provider=codex", "--", "{prompt}"})
	if got != "codex" {
		t.Errorf("resolveProvider() = %q, want \"codex\"", got)
	}
}

func TestResolveProviderStopsAtSeparator(t *testing.T) {
	// --provider appearing AFTER "--" is a provider-arg, not a clown flag —
	// must not be mistaken for the clown-level provider selector.
	got := resolveProvider([]string{"clown", "--", "--provider", "not-a-clown-flag"})
	if got != "claude" {
		t.Errorf("resolveProvider() = %q, want \"claude\" (post-separator --provider ignored)", got)
	}
}

// TestResolveProviderProfileSpaceForm covers --profile <name>: clown's
// named-profile registry implies a provider that spinclass can't resolve
// textually (it would require querying clown's own profiles.toml), so the
// profile name itself becomes the model-flags lookup key.
func TestResolveProviderProfileSpaceForm(t *testing.T) {
	got := resolveProvider([]string{"clown", "--profile", "my-juggler", "--", "{prompt}"})
	if got != "my-juggler" {
		t.Errorf("resolveProvider() = %q, want \"my-juggler\"", got)
	}
}

func TestResolveProviderProfileEqualsForm(t *testing.T) {
	got := resolveProvider([]string{"clown", "--profile=my-juggler", "--", "{prompt}"})
	if got != "my-juggler" {
		t.Errorf("resolveProvider() = %q, want \"my-juggler\"", got)
	}
}

// TestResolveProviderProviderWinsOverProfile matches clown's own documented
// precedence (clownfile(5) [profile].profile: "an explicit --provider
// suppresses the pin ... but never an explicit --profile" — i.e. an
// explicit --provider on the command line is authoritative over a profile).
func TestResolveProviderProviderWinsOverProfile(t *testing.T) {
	got := resolveProvider([]string{"clown", "--profile", "my-juggler", "--provider", "codex", "--", "{prompt}"})
	if got != "codex" {
		t.Errorf("resolveProvider() = %q, want \"codex\" (explicit --provider wins over --profile)", got)
	}
}

func TestResolveProviderProfileStopsAtSeparator(t *testing.T) {
	got := resolveProvider([]string{"clown", "--", "--profile", "not-a-clown-flag"})
	if got != "claude" {
		t.Errorf("resolveProvider() = %q, want \"claude\" (post-separator --profile ignored)", got)
	}
}

func TestSpliceModelFlagInsertsAfterSeparator(t *testing.T) {
	entry := []string{"clown", "--clown-attach=spawn", "--", "{prompt}"}
	flags := map[string]string{"claude": "--model"}
	got, err := SpliceModelFlag(entry, "opus", flags, testClaudeModelIDs)
	if err != nil {
		t.Fatalf("SpliceModelFlag: %v", err)
	}
	want := []string{"clown", "--clown-attach=spawn", "--", "--model", "claude-opus-5", "{prompt}"}
	if len(got) != len(want) {
		t.Fatalf("SpliceModelFlag() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SpliceModelFlag()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSpliceModelFlagRewritesAllClaudeAliases proves every alias in the
// configured modelIDs map is rewritten to its mapped full ID for the claude
// provider, not just "opus" (which the other tests happen to use).
func TestSpliceModelFlagRewritesAllClaudeAliases(t *testing.T) {
	flags := map[string]string{"claude": "--model"}
	for alias, id := range testClaudeModelIDs {
		entry := []string{"clown", "--", "{prompt}"}
		got, err := SpliceModelFlag(entry, alias, flags, testClaudeModelIDs)
		if err != nil {
			t.Fatalf("SpliceModelFlag(%q): %v", alias, err)
		}
		want := []string{"clown", "--", "--model", id, "{prompt}"}
		if len(got) != len(want) {
			t.Fatalf("SpliceModelFlag(%q) = %v, want %v", alias, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("SpliceModelFlag(%q)[%d] = %q, want %q", alias, i, got[i], want[i])
			}
		}
	}
}

func TestSpliceModelFlagCustomProvider(t *testing.T) {
	entry := []string{"clown", "--provider=codex", "--", "{prompt}"}
	flags := map[string]string{"claude": "--model", "codex": "--model-name"}
	got, err := SpliceModelFlag(entry, "opus", flags, testClaudeModelIDs)
	if err != nil {
		t.Fatalf("SpliceModelFlag: %v", err)
	}
	// Non-claude provider: the raw alias is spliced verbatim, NOT rewritten
	// via modelIDs — modelIDs is a Claude-only alias->ID map.
	want := []string{"clown", "--provider=codex", "--", "--model-name", "opus", "{prompt}"}
	if len(got) != len(want) || got[3] != want[3] || got[4] != want[4] {
		t.Errorf("SpliceModelFlag() = %v, want %v", got, want)
	}
}

func TestSpliceModelFlagNoSeparatorErrors(t *testing.T) {
	entry := []string{"my-harness", "{prompt}"}
	_, err := SpliceModelFlag(entry, "opus", map[string]string{"claude": "--model"}, testClaudeModelIDs)
	if err == nil {
		t.Fatal("SpliceModelFlag() = nil error, want error (no \"--\" in entry)")
	}
}

func TestSpliceModelFlagUnmappedProviderErrors(t *testing.T) {
	entry := []string{"clown", "--provider=circus", "--", "{prompt}"}
	_, err := SpliceModelFlag(entry, "opus", map[string]string{"claude": "--model"}, testClaudeModelIDs)
	if err == nil {
		t.Fatal("SpliceModelFlag() = nil error, want error (circus not in flags map)")
	}
}

// TestSpliceModelFlagRejectsBadAliasForClaudeProvider is a regression pin:
// SpliceModelFlag now validates internally, and the default/claude case must
// keep rejecting unrecognized aliases exactly as before.
func TestSpliceModelFlagRejectsBadAliasForClaudeProvider(t *testing.T) {
	entry := []string{"clown", "--clown-attach=spawn", "--", "{prompt}"}
	_, err := SpliceModelFlag(entry, "gpt5", map[string]string{"claude": "--model"}, testClaudeModelIDs)
	if err == nil {
		t.Fatal("SpliceModelFlag() = nil error, want error (gpt5 is not a known Claude alias)")
	}
	if !strings.Contains(err.Error(), "unrecognized model") {
		t.Errorf("error = %q, want it to mention the unrecognized model", err.Error())
	}
}

// TestSpliceModelFlagAllowsArbitraryAliasForNonClaudeProvider proves the
// juggler/clownbox composition case: a model name that would never pass the
// fixed Claude alias set (a GGUF-style name, an OpenRouter model id, ...)
// must still splice successfully, unrewritten, once the provider isn't
// "claude".
func TestSpliceModelFlagAllowsArbitraryAliasForNonClaudeProvider(t *testing.T) {
	entry := []string{"clown", "--provider=juggler", "--", "{prompt}"}
	flags := map[string]string{"claude": "--model", "juggler": "--model"}
	got, err := SpliceModelFlag(entry, "llama-3-70b-instruct.Q4_K_M", flags, testClaudeModelIDs)
	if err != nil {
		t.Fatalf("SpliceModelFlag: %v", err)
	}
	want := []string{"clown", "--provider=juggler", "--", "--model", "llama-3-70b-instruct.Q4_K_M", "{prompt}"}
	if len(got) != len(want) {
		t.Fatalf("SpliceModelFlag() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SpliceModelFlag()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSpliceModelFlagResolvesProviderFromProfile proves model-flags entries
// can be keyed by clown profile name, not just provider name, since a
// --profile-selected provider can't be resolved from argv text alone.
func TestSpliceModelFlagResolvesProviderFromProfile(t *testing.T) {
	entry := []string{"clown", "--profile", "my-juggler", "--", "{prompt}"}
	flags := map[string]string{"claude": "--model", "my-juggler": "--model"}
	got, err := SpliceModelFlag(entry, "some-local-model", flags, testClaudeModelIDs)
	if err != nil {
		t.Fatalf("SpliceModelFlag: %v", err)
	}
	want := []string{"clown", "--profile", "my-juggler", "--", "--model", "some-local-model", "{prompt}"}
	if len(got) != len(want) {
		t.Fatalf("SpliceModelFlag() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SpliceModelFlag()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSpliceModelFlagDoesNotMutateInput(t *testing.T) {
	entry := []string{"clown", "--", "{prompt}"}
	orig := append([]string(nil), entry...)
	if _, err := SpliceModelFlag(entry, "opus", map[string]string{"claude": "--model"}, testClaudeModelIDs); err != nil {
		t.Fatal(err)
	}
	for i := range orig {
		if entry[i] != orig[i] {
			t.Errorf("input entry mutated: entry[%d] = %q, want %q", i, entry[i], orig[i])
		}
	}
}
