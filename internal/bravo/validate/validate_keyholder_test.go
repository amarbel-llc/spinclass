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
