package run

import "testing"

func TestParseArgsNoRegenerate(t *testing.T) {
	spec, err := ParseArgs([]string{"--no-regenerate", "--", "true"}, nil)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if !spec.NoRegenerate {
		t.Error("--no-regenerate did not set NoRegenerate")
	}
	def, err := ParseArgs([]string{"--", "true"}, nil)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if def.NoRegenerate {
		t.Error("the phase must be on by default")
	}
}
