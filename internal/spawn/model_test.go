package spawn

import "testing"

func TestValidateModelAliasKnown(t *testing.T) {
	for _, alias := range []string{"sonnet", "opus", "haiku", "fable"} {
		if err := ValidateModelAlias(alias); err != nil {
			t.Errorf("ValidateModelAlias(%q) = %v, want nil", alias, err)
		}
	}
}

func TestValidateModelAliasUnknown(t *testing.T) {
	if err := ValidateModelAlias("gpt5"); err == nil {
		t.Error("ValidateModelAlias(\"gpt5\") = nil, want error")
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

func TestSpliceModelFlagInsertsAfterSeparator(t *testing.T) {
	entry := []string{"clown", "--clown-attach=spawn", "--", "{prompt}"}
	flags := map[string]string{"claude": "--model"}
	got, err := SpliceModelFlag(entry, "opus", flags)
	if err != nil {
		t.Fatalf("SpliceModelFlag: %v", err)
	}
	want := []string{"clown", "--clown-attach=spawn", "--", "--model", "opus", "{prompt}"}
	if len(got) != len(want) {
		t.Fatalf("SpliceModelFlag() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SpliceModelFlag()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSpliceModelFlagCustomProvider(t *testing.T) {
	entry := []string{"clown", "--provider=codex", "--", "{prompt}"}
	flags := map[string]string{"claude": "--model", "codex": "--model-name"}
	got, err := SpliceModelFlag(entry, "opus", flags)
	if err != nil {
		t.Fatalf("SpliceModelFlag: %v", err)
	}
	want := []string{"clown", "--provider=codex", "--", "--model-name", "opus", "{prompt}"}
	if len(got) != len(want) || got[3] != want[3] || got[4] != want[4] {
		t.Errorf("SpliceModelFlag() = %v, want %v", got, want)
	}
}

func TestSpliceModelFlagNoSeparatorErrors(t *testing.T) {
	entry := []string{"my-harness", "{prompt}"}
	_, err := SpliceModelFlag(entry, "opus", map[string]string{"claude": "--model"})
	if err == nil {
		t.Fatal("SpliceModelFlag() = nil error, want error (no \"--\" in entry)")
	}
}

func TestSpliceModelFlagUnmappedProviderErrors(t *testing.T) {
	entry := []string{"clown", "--provider=circus", "--", "{prompt}"}
	_, err := SpliceModelFlag(entry, "opus", map[string]string{"claude": "--model"})
	if err == nil {
		t.Fatal("SpliceModelFlag() = nil error, want error (circus not in flags map)")
	}
}

func TestSpliceModelFlagDoesNotMutateInput(t *testing.T) {
	entry := []string{"clown", "--", "{prompt}"}
	orig := append([]string(nil), entry...)
	if _, err := SpliceModelFlag(entry, "opus", map[string]string{"claude": "--model"}); err != nil {
		t.Fatal(err)
	}
	for i := range orig {
		if entry[i] != orig[i] {
			t.Errorf("input entry mutated: entry[%d] = %q, want %q", i, entry[i], orig[i])
		}
	}
}
