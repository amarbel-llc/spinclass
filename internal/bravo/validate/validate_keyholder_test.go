package validate

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// [keyholder] is the operator's switch: a repo-level table warns once and a
// trusted one does not; an invalid mode is an error wherever it is.
func TestCheckKeyholder(t *testing.T) {
	on, bad := "on", "yes"
	sf := sweatfile.Sweatfile{Keyholder: &sweatfile.Keyholder{Mode: &on}}
	if got := CheckKeyholder(sf, false); len(got) != 1 || got[0].Field != "keyholder" || got[0].Severity != SeverityWarning {
		t.Errorf("repo-layer [keyholder] should warn once, got %+v", got)
	}
	if got := CheckKeyholder(sf, true); len(got) != 0 {
		t.Errorf("above-repo valid [keyholder] should be silent, got %+v", got)
	}
	if got := CheckKeyholder(sweatfile.Sweatfile{}, false); len(got) != 0 {
		t.Errorf("no [keyholder] table should be silent, got %+v", got)
	}
	got := CheckKeyholder(sweatfile.Sweatfile{Keyholder: &sweatfile.Keyholder{Mode: &bad}}, true)
	if len(got) != 1 || got[0].Severity != SeverityError || !strings.Contains(got[0].Message, `"yes"`) {
		t.Errorf("an invalid mode should be one error naming the value, got %+v", got)
	}
}

// A [keyholder] fibby or piggy no store program could satisfy is a warning while
// the layer's mode is not "on" and an error when it is: before the switch is
// turned on nothing is launched, after it every launch would be refused.
func TestCheckKeyholderToolPaths(t *testing.T) {
	on, off := "on", "off"
	rel, outside := "bin/fibby", "/usr/bin/piggy"
	cases := []struct {
		name     string
		kh       sweatfile.Keyholder
		wantSev  string
		wantMsg  string
		wantNone bool
	}{
		{"relative fibby, mode on", sweatfile.Keyholder{Mode: &on, Fibby: &rel}, SeverityError, "[keyholder] fibby", false},
		{"outside the store piggy, mode on", sweatfile.Keyholder{Mode: &on, Piggy: &outside}, SeverityError, "[keyholder] piggy", false},
		{"relative fibby, mode off", sweatfile.Keyholder{Mode: &off, Fibby: &rel}, SeverityWarning, "[keyholder] fibby", false},
		{"relative fibby, mode unset", sweatfile.Keyholder{Fibby: &rel}, SeverityWarning, "[keyholder] fibby", false},
		{"nothing set", sweatfile.Keyholder{Mode: &on}, "", "", true},
	}
	for _, c := range cases {
		kh := c.kh
		got := CheckKeyholder(sweatfile.Sweatfile{Keyholder: &kh}, true)
		if c.wantNone {
			if len(got) != 0 {
				t.Errorf("%s: got %+v, want nothing", c.name, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Severity != c.wantSev || !strings.Contains(got[0].Message, c.wantMsg) {
			t.Errorf("%s: got %+v, want one %s naming %q", c.name, got, c.wantSev, c.wantMsg)
		}
	}
}
