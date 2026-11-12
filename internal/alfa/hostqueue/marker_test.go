package hostqueue

import (
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/hostload"
)

// host-load finds sessions that run gates unqueued by searching their serve
// binary for AdmissionMarker; it must stay inside the text Decide emits.
func TestAdmissionMarkerIsInTheGateCountRefusal(t *testing.T) {
	v := View{Live: []Marker{{Session: "a/b"}}}
	d := Decide(v, hostload.Snapshot{}, DefaultConfig(), WaiterState{}, time.Now())
	if d.Reason != ReasonGates || !strings.Contains(d.Detail, AdmissionMarker) {
		t.Fatalf("detail %q (reason %s) must contain %q", d.Detail, d.Reason, AdmissionMarker)
	}
}
