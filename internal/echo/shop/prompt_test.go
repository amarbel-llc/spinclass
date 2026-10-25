package shop

import "testing"

func TestDirtyActionStringValues(t *testing.T) {
	tests := []struct {
		action dirtyAction
		want   string
	}{
		{actionDiscard, "Discard changes and merge"},
		{actionReattach, "Reattach to session"},
		{actionExit, "Exit without integrating"},
	}
	for _, tt := range tests {
		if got := string(tt.action); got != tt.want {
			t.Errorf("dirtyAction = %q, want %q", got, tt.want)
		}
	}
}

// TestParseAutocloseAssume covers the env-var bypass added for #66:
// each recognized form maps to a (proceed, set) pair; unrecognized
// or empty values fall back to (false, false) so the caller drops
// into the existing interactive prompt path.
func TestParseAutocloseAssume(t *testing.T) {
	tests := []struct {
		raw     string
		proceed bool
		set     bool
	}{
		{"yes", true, true},
		{"YES", true, true},
		{"y", true, true},
		{"true", true, true},
		{"True", true, true},
		{"1", true, true},
		{"no", false, true},
		{"NO", false, true},
		{"n", false, true},
		{"false", false, true},
		{"False", false, true},
		{"0", false, true},
		{"  yes  ", true, true},
		{"", false, false},
		{"maybe", false, false},
		{"2", false, false},
	}
	for _, tt := range tests {
		t.Setenv(EnvAutocloseAssume, tt.raw)
		proceed, set := parseAutocloseAssume()
		if proceed != tt.proceed || set != tt.set {
			t.Errorf("parseAutocloseAssume(%q) = (%v, %v), want (%v, %v)",
				tt.raw, proceed, set, tt.proceed, tt.set)
		}
	}
}

// TestParseAutocloseAssumeUnset covers the truly-unset case (vs the
// empty-string case t.Setenv exercises).
func TestParseAutocloseAssumeUnset(t *testing.T) {
	t.Setenv(EnvAutocloseAssume, "")
	proceed, set := parseAutocloseAssume()
	if proceed || set {
		t.Errorf("parseAutocloseAssume() unset = (%v, %v), want (false, false)", proceed, set)
	}
}
