package spawn

import (
	"reflect"
	"strings"
	"testing"
)

func TestSpliceDontAskModeReplacesAutoAndExistingModes(t *testing.T) {
	in := []string{"clown", "--clown-attach=spawn", "--", "--enable-auto-mode", "--permission-mode", "acceptEdits", "--model", "x", "--permission-mode=plan", "the brief"}
	got, err := SpliceDontAskMode(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"clown", "--clown-attach=spawn", "--", "--permission-mode", "dontAsk", "--model", "x", "the brief"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if in[3] != "--enable-auto-mode" {
		t.Error("the input was mutated")
	}
}

func TestSpliceDontAskModeFailsClosed(t *testing.T) {
	if _, err := SpliceDontAskMode([]string{"clown", "brief"}); err == nil || !strings.Contains(err.Error(), "separator") {
		t.Errorf("no separator: got %v", err)
	}
	if _, err := SpliceDontAskMode([]string{"clown", "--provider", "codex", "--", "brief"}); err == nil || !strings.Contains(err.Error(), "not claude") {
		t.Errorf("non-claude provider: got %v", err)
	}
}

func TestSpliceDontAskModeAddsTheModeWhenNoneWasThere(t *testing.T) {
	got, err := SpliceDontAskMode([]string{"clown", "--", "brief"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"clown", "--", "--permission-mode", "dontAsk", "brief"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}
