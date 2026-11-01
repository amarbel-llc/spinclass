package sweatfile_test

import (
	"reflect"
	"testing"

	. "code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// [folio] (spinclass#438) decodes its three pattern lists and the unmatched
// rule, and like [permissions] it never survives MergeWith: policy is read
// only through sweatfileio.TrustedFolio.
func TestFolioDecodesAndIsNeverMerged(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte(`
[folio]
read = ["pwd:", "spinclass:children"]
write = ["pwd:"]
deny = ["spinclass:worktrees/*/.spinclass/state.json"]
unmatched = "ask"
`))
	if err != nil {
		t.Fatal(err)
	}
	sf := doc.Data()
	if sf.Folio == nil {
		t.Fatal("[folio] did not decode")
	}
	if !reflect.DeepEqual(sf.Folio.Read, []string{"pwd:", "spinclass:children"}) ||
		!reflect.DeepEqual(sf.Folio.Write, []string{"pwd:"}) ||
		!reflect.DeepEqual(sf.Folio.Deny, []string{"spinclass:worktrees/*/.spinclass/state.json"}) ||
		sf.Folio.Unmatched == nil || *sf.Folio.Unmatched != "ask" {
		t.Errorf("decoded %+v", sf.Folio)
	}

	if merged := sf.MergeWith(Sweatfile{Folio: &Folio{Read: []string{"pwd:"}}}); merged.Folio != nil {
		t.Errorf("[folio] leaked through MergeWith: %+v", merged.Folio)
	}
	if merged := (Sweatfile{}).MergeWith(*sf); merged.Folio != nil {
		t.Errorf("[folio] leaked through MergeWith: %+v", merged.Folio)
	}
}
