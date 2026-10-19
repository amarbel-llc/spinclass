package profilegrant

import (
	"strings"
	"testing"
)

func TestWalkRejectsResolverReturningWrongRecord(t *testing.T) {
	records, leaf := threeLinkChain()
	resolve := func(string) (Record, bool) { return records[1], true } // always a
	want := "resolver returned digest " + records[1].Digest + " for parent " + records[2].Digest
	_, err := Walk(now, leaf, resolve, signedTextVerifier(), WalkOptions{})
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error %v", err)
	}
}
