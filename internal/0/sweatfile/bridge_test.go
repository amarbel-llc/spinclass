package sweatfile_test

import (
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// [confinement] bridge decodes as a string and, like the rest of
// [confinement], never survives MergeWith: it is read only through
// sweatfileio.TrustedConfinement.
func TestConfinementBridgeDecodesAndIsNeverMerged(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[confinement]\nbridge = \"offered\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	sf := doc.Data()
	if sf.Confinement == nil || sf.Confinement.Bridge == nil || *sf.Confinement.Bridge != "offered" {
		t.Fatalf("[confinement] bridge did not decode: %+v", sf.Confinement)
	}
	if merged := sf.MergeWith(sweatfile.Sweatfile{}); merged.Confinement != nil {
		t.Errorf("[confinement] leaked through MergeWith: %+v", merged.Confinement)
	}
	if merged := (sweatfile.Sweatfile{}).MergeWith(*sf); merged.Confinement != nil {
		t.Errorf("[confinement] leaked through MergeWith: %+v", merged.Confinement)
	}
}
