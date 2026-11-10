package sweatfile_test

import (
	"testing"

	. "code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// [host-queue] (#424) decodes, and like [health] it never survives MergeWith:
// it is read only through sweatfileio.TrustedHostQueueRecordCosts.
func TestHostQueueDecodesAndIsNeverMerged(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[host-queue]\nrecord-costs = false\n"))
	if err != nil {
		t.Fatal(err)
	}
	sf := doc.Data()
	if sf.HostQueue == nil || sf.HostQueue.RecordCosts == nil || *sf.HostQueue.RecordCosts {
		t.Fatalf("[host-queue] did not decode: %+v", sf.HostQueue)
	}
	if merged := sf.MergeWith(Sweatfile{}); merged.HostQueue != nil {
		t.Errorf("[host-queue] leaked through MergeWith: %+v", merged.HostQueue)
	}
	if merged := (Sweatfile{}).MergeWith(*sf); merged.HostQueue != nil {
		t.Errorf("[host-queue] leaked through MergeWith: %+v", merged.HostQueue)
	}
}
