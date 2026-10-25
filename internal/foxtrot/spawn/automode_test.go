package spawn

import (
	"reflect"
	"testing"
)

func TestSpliceAutoModeInsertsForClaudeProvider(t *testing.T) {
	entry := []string{"clown", "--clown-attach=spawn", "--", "{prompt}"}
	got := SpliceAutoMode(entry)
	want := []string{"clown", "--clown-attach=spawn", "--", "--permission-mode", "auto", "{prompt}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SpliceAutoMode() = %v, want %v", got, want)
	}
}

// An entry that already names a permission mode chose it on purpose; the
// default must not override it.
func TestSpliceAutoModeKeepsAnExplicitMode(t *testing.T) {
	for _, entry := range [][]string{
		{"clown", "--", "--permission-mode", "plan", "{prompt}"},
		{"clown", "--", "--permission-mode=plan", "{prompt}"},
	} {
		if got := SpliceAutoMode(entry); !reflect.DeepEqual(got, entry) {
			t.Errorf("SpliceAutoMode(%v) = %v, want it unmodified", entry, got)
		}
	}
}

// The retired --enable-auto-mode (absent from Claude Code 2.1.301's help) is
// replaced by the real mode, not left beside it.
func TestSpliceAutoModeReplacesTheRetiredFlag(t *testing.T) {
	got := SpliceAutoMode([]string{"clown", "--", "--enable-auto-mode", "{prompt}"})
	want := []string{"clown", "--", "--permission-mode", "auto", "{prompt}"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SpliceAutoMode() = %v, want %v", got, want)
	}
}

// TestSpliceAutoModeNoOpForNonClaudeProvider pins that, unlike
// SpliceModelFlag, this is claude-only with no per-provider flag map: a
// non-claude provider's entry is left completely unmodified.
func TestSpliceAutoModeNoOpForNonClaudeProvider(t *testing.T) {
	entry := []string{"clown", "--provider=codex", "--", "{prompt}"}
	if got := SpliceAutoMode(entry); !reflect.DeepEqual(got, entry) {
		t.Fatalf("SpliceAutoMode() = %v, want unmodified %v", got, entry)
	}
}

// TestSpliceAutoModeNoOpWithoutSeparator pins that, unlike SpliceModelFlag, a
// missing "--" is a silent no-op, not an error — this is an automatic default
// applied to every spawn, so a fully custom spawn-entry (a different harness
// entirely) must not be broken by it.
func TestSpliceAutoModeNoOpWithoutSeparator(t *testing.T) {
	entry := []string{"my-harness", "{prompt}"}
	if got := SpliceAutoMode(entry); !reflect.DeepEqual(got, entry) {
		t.Errorf("SpliceAutoMode() = %v, want unmodified %v", got, entry)
	}
}

func TestSpliceAutoModeDoesNotMutateInput(t *testing.T) {
	entry := []string{"clown", "--", "--enable-auto-mode", "{prompt}"}
	orig := append([]string(nil), entry...)
	SpliceAutoMode(entry)
	if !reflect.DeepEqual(entry, orig) {
		t.Errorf("input entry mutated: %v, want %v", entry, orig)
	}
}

// A confined spawn replaces the auto mode with dontAsk.
func TestSpliceDontAskModeReplacesTheAutoSplice(t *testing.T) {
	got, err := SpliceDontAskMode(SpliceAutoMode([]string{"clown", "--", "brief"}))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"clown", "--", "--permission-mode", "dontAsk", "brief"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}
