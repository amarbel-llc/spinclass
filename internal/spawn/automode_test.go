package spawn

import "testing"

func TestSpliceAutoModeFlagInsertsForClaudeProvider(t *testing.T) {
	entry := []string{"clown", "--clown-attach=spawn", "--", "{prompt}"}
	got := SpliceAutoModeFlag(entry)
	want := []string{"clown", "--clown-attach=spawn", "--", "--enable-auto-mode", "{prompt}"}
	if len(got) != len(want) {
		t.Fatalf("SpliceAutoModeFlag() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SpliceAutoModeFlag()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSpliceAutoModeFlagNoOpForNonClaudeProvider pins that, unlike
// SpliceModelFlag, this is claude-only with no per-provider flag map: a
// non-claude provider's entry is left completely unmodified.
func TestSpliceAutoModeFlagNoOpForNonClaudeProvider(t *testing.T) {
	entry := []string{"clown", "--provider=codex", "--", "{prompt}"}
	got := SpliceAutoModeFlag(entry)
	if len(got) != len(entry) {
		t.Fatalf("SpliceAutoModeFlag() = %v, want unmodified %v", got, entry)
	}
	for i := range entry {
		if got[i] != entry[i] {
			t.Errorf("SpliceAutoModeFlag()[%d] = %q, want %q", i, got[i], entry[i])
		}
	}
}

// TestSpliceAutoModeFlagNoOpWithoutSeparator pins that, unlike
// SpliceModelFlag, a missing "--" is a silent no-op, not an error — this is
// an automatic default applied to every spawn, so a fully custom
// spawn-entry (a different harness entirely) must not be broken by it.
func TestSpliceAutoModeFlagNoOpWithoutSeparator(t *testing.T) {
	entry := []string{"my-harness", "{prompt}"}
	got := SpliceAutoModeFlag(entry)
	if len(got) != len(entry) || got[0] != entry[0] || got[1] != entry[1] {
		t.Errorf("SpliceAutoModeFlag() = %v, want unmodified %v", got, entry)
	}
}

func TestSpliceAutoModeFlagDoesNotMutateInput(t *testing.T) {
	entry := []string{"clown", "--", "{prompt}"}
	orig := append([]string(nil), entry...)
	SpliceAutoModeFlag(entry)
	for i := range orig {
		if entry[i] != orig[i] {
			t.Errorf("input entry mutated: entry[%d] = %q, want %q", i, entry[i], orig[i])
		}
	}
}
