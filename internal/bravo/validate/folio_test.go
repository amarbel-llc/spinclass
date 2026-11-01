package validate

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

func strp(s string) *string { return &s }

// [folio] (spinclass#438): every entry must parse under the stanza grammar
// (an unparseable entry is an ERROR, because an ignored deny would widen);
// unmatched must be ask or deny; and the allow lists and unmatched are honoured
// only above the repo, so a repo-level value warns as ignored. A repo-level
// deny is fine: it only narrows.
func TestCheckFolio(t *testing.T) {
	good := sweatfile.Sweatfile{Folio: &sweatfile.Folio{
		Read:      []string{"pwd:", "spinclass:children"},
		Write:     []string{"home:.claude/plans"},
		Deny:      []string{"spinclass:worktrees/*/.spinclass/state.json"},
		Unmatched: strp("ask"),
	}}
	if got := CheckFolio(good, true); len(got) != 0 {
		t.Errorf("a valid stanza above the repo should pass, got %+v", got)
	}
	if got := CheckFolio(sweatfile.Sweatfile{}, false); len(got) != 0 {
		t.Errorf("absent table should not warn, got %+v", got)
	}

	got := CheckFolio(good, false)
	if len(got) != 3 {
		t.Fatalf("repo-level read, write and unmatched should each warn once, got %+v", got)
	}
	for _, iss := range got {
		if iss.Severity != SeverityWarning || !strings.Contains(iss.Message, "ignored") {
			t.Errorf("repo-level allow should be a warning saying it is ignored, got %+v", iss)
		}
	}
	if got := CheckFolio(sweatfile.Sweatfile{Folio: &sweatfile.Folio{Deny: []string{"pwd:secret"}}}, false); len(got) != 0 {
		t.Errorf("a repo-level deny is honoured and should not warn, got %+v", got)
	}

	bad := sweatfile.Sweatfile{Folio: &sweatfile.Folio{
		Read:      []string{"pwd:../up"},
		Deny:      []string{"nowhere:x"},
		Unmatched: strp("allow"),
	}}
	got = CheckFolio(bad, true)
	if len(got) != 3 {
		t.Fatalf("want one error per bad read, bad deny and bad unmatched, got %+v", got)
	}
	for _, iss := range got {
		if iss.Severity != SeverityError {
			t.Errorf("a bad entry is an error, got %+v", iss)
		}
	}
	if got[0].Field != "folio.read" || got[1].Field != "folio.deny" || got[2].Field != "folio.unmatched" {
		t.Errorf("fields = %q %q %q", got[0].Field, got[1].Field, got[2].Field)
	}
	if got[0].Value != "pwd:../up" {
		t.Errorf("the offending entry should be the issue's value, got %q", got[0].Value)
	}
}
