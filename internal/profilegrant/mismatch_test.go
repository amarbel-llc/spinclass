package profilegrant

import (
	"strings"
	"testing"
)

func TestWalkRejectsResolverReturningWrongRecord(t *testing.T) {
	records, leaf := threeLinkChain()
	resolve := func(string) (Record, bool) { return records[1], true } // always d-a
	_, err := Walk(now, leaf, resolve, fakeVerifier{})
	if err == nil || !strings.Contains(err.Error(), "resolver returned digest d-a for parent d-b") {
		t.Fatalf("error %v", err)
	}
}
