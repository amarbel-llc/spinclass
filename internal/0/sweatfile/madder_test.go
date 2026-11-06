package sweatfile

import (
	"testing"
	"time"
)

// [madder].retain parses to a positive duration; everything else (unset, empty,
// zero, negative, unparseable) is the keep-forever default (spinclass#427).
func TestAttestationRetention(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name   string
		sf     Sweatfile
		wantD  time.Duration
		wantOK bool
	}{
		{"no table", Sweatfile{}, 0, false},
		{"nil retain", Sweatfile{Madder: &Madder{}}, 0, false},
		{"empty", Sweatfile{Madder: &Madder{Retain: str("")}}, 0, false},
		{"zero", Sweatfile{Madder: &Madder{Retain: str("0")}}, 0, false},
		{"negative", Sweatfile{Madder: &Madder{Retain: str("-1h")}}, 0, false},
		{"garbage", Sweatfile{Madder: &Madder{Retain: str("a month")}}, 0, false},
		{"set", Sweatfile{Madder: &Madder{Retain: str("720h")}}, 720 * time.Hour, true},
	}
	for _, tc := range cases {
		d, ok := tc.sf.AttestationRetention()
		if d != tc.wantD || ok != tc.wantOK {
			t.Errorf("%s: AttestationRetention() = (%v, %v), want (%v, %v)", tc.name, d, ok, tc.wantD, tc.wantOK)
		}
	}
}

// A child layer's [madder].retain overrides the parent's; a parent's value
// survives a child with no table; the receiver's table is never mutated.
func TestMergeWithMadderIsScalarOverride(t *testing.T) {
	str := func(s string) *string { return &s }
	parent := Sweatfile{Madder: &Madder{Retain: str("720h")}}
	child := Sweatfile{Madder: &Madder{Retain: str("24h")}}
	merged := parent.MergeWith(child)
	if d, ok := merged.AttestationRetention(); !ok || d != 24*time.Hour {
		t.Errorf("child override: got (%v, %v)", d, ok)
	}
	if d, _ := parent.AttestationRetention(); d != 720*time.Hour {
		t.Errorf("parent mutated: %v", d)
	}
	if d, ok := parent.MergeWith(Sweatfile{}).AttestationRetention(); !ok || d != 720*time.Hour {
		t.Errorf("parent value lost under an empty child: got (%v, %v)", d, ok)
	}
}
