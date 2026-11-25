package sweatfile_test

import (
	"testing"

	. "code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// [bridge] decodes, and like [health] it never survives MergeWith: it is read
// only through sweatfileio.TrustedBridgeEnabled.
func TestBridgeDecodesAndIsNeverMerged(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[bridge]\nenable = true\n"))
	if err != nil {
		t.Fatal(err)
	}
	sf := doc.Data()
	if sf.Bridge == nil || sf.Bridge.Enable == nil || !*sf.Bridge.Enable {
		t.Fatalf("[bridge] did not decode: %+v", sf.Bridge)
	}
	if merged := sf.MergeWith(Sweatfile{}); merged.Bridge != nil {
		t.Errorf("[bridge] leaked through MergeWith: %+v", merged.Bridge)
	}
	if merged := (Sweatfile{}).MergeWith(*sf); merged.Bridge != nil {
		t.Errorf("[bridge] leaked through MergeWith: %+v", merged.Bridge)
	}
}
