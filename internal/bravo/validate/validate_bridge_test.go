package validate

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

func strPtr(s string) *string { return &s }

// [confinement] bridge is the ONE switch of the bridge: "off", "offered" and
// "required" are valid from a layer above the repo; any other value is an error
// that names what is allowed (the spawn refuses it); a repo layer is told it is
// ignored.
func TestCheckConfinementBridge(t *testing.T) {
	for _, v := range []string{"off", "offered", "required"} {
		sf := sweatfile.Sweatfile{Confinement: &sweatfile.Confinement{Bridge: strPtr(v)}}
		if got := CheckConfinement(sf, true, true); len(got) != 0 {
			t.Errorf("bridge = %q from a trusted layer should be clean, got %+v", v, got)
		}
	}
	bad := sweatfile.Sweatfile{Confinement: &sweatfile.Confinement{Bridge: strPtr("on")}}
	got := CheckConfinement(bad, true, true)
	if len(got) != 1 || got[0].Severity != SeverityError || got[0].Field != "confinement.bridge" ||
		!strings.Contains(got[0].Message, `"on"`) || !strings.Contains(got[0].Message, "offered") || !strings.Contains(got[0].Message, "required") {
		t.Errorf("an unknown bridge value should be one error naming the value and the allowed ones, got %+v", got)
	}
	repoLayer := sweatfile.Sweatfile{Confinement: &sweatfile.Confinement{Bridge: strPtr("required")}}
	got = CheckConfinement(repoLayer, false, false)
	if len(got) != 1 || got[0].Severity != SeverityWarning || !strings.Contains(got[0].Message, "ignored") {
		t.Errorf("a repo-layer bridge should warn once that it is ignored, got %+v", got)
	}
}

// The removed [bridge] enable stanza is an unknown field like any other, and
// the error says what replaced it so an operator who still has it learns the
// one switch.
func TestCheckUnknownFieldsNamesTheReplacementOfTheRemovedBridgeStanza(t *testing.T) {
	issues := CheckUnknownFields([]byte("[bridge]\nenable = true\n"))
	if len(issues) == 0 {
		t.Fatal("the removed [bridge] stanza must be reported, not silently accepted")
	}
	for _, iss := range issues {
		if iss.Severity != SeverityError {
			t.Errorf("%+v: want error severity", iss)
		}
		if !strings.Contains(iss.Message, "[confinement] bridge") {
			t.Errorf("%+v: the message must name the replacement [confinement] bridge", iss)
		}
	}
	// Other unknown fields keep the plain message.
	plain := CheckUnknownFields([]byte("unknown_field = \"x\"\n"))
	if len(plain) != 1 || strings.Contains(plain[0].Message, "bridge") {
		t.Errorf("an unrelated unknown field got %+v", plain)
	}
}
