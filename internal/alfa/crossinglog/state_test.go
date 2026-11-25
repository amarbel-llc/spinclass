package crossinglog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStateRoundTripsWithOwnerOnlyModeAndNoTempLeft(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spinclass", "bridge")
	want := State{Session: "repo/branch", Listening: false, Reason: "the unit clown named is not the recorded one", Since: time.Unix(100, 0).UTC(), UpdatedAt: time.Unix(200, 0).UTC()}
	if err := WriteState(dir, "principal-1", want); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadState(dir, "principal-1")
	if !ok || got.Session != "repo/branch" || got.Listening || got.Reason != want.Reason || !got.Since.Equal(want.Since) {
		t.Fatalf("ReadState = %+v, %v", got, ok)
	}
	fi, err := os.Stat(StatePath(dir, "principal-1"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode: %v %v", fi, err)
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Errorf("state dir mode %o", di.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("the directory holds %d entries after a write, want only the state file: %v", len(entries), entries)
	}
	if got.Note() != "bridge DOWN: "+want.Reason {
		t.Errorf("Note = %q", got.Note())
	}
	if (State{Listening: true}).Note() != "" {
		t.Error("a listening bridge has nothing to note")
	}
}

func TestStateReasonIsBoundedAndControlFree(t *testing.T) {
	dir := t.TempDir()
	if err := WriteState(dir, "p", State{Reason: "line one\nESC\x1b[31m injected ‮" + strings.Repeat("x", 1000)}); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadState(dir, "p")
	if !ok {
		t.Fatal("no state read back")
	}
	if strings.ContainsAny(got.Reason, "\n\x1b‮") || len([]rune(got.Reason)) > maxStateReason+3 {
		t.Errorf("reason is not safe or bounded: %q", got.Reason)
	}
}

func TestReadStateRefusesWhatItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadState(dir, "absent"); ok {
		t.Error("an absent file read as a state")
	}
	target := filepath.Join(dir, "real.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"session":"s","listening":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, StatePath(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadState(dir, "linked"); ok {
		t.Error("a symlinked state file was followed")
	}
	for name, body := range map[string]string{"junk": "not json", "wrong version": `{"version":2}`, "huge": strings.Repeat(" ", maxStateBytes+10)} {
		if err := os.WriteFile(StatePath(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := ReadState(dir, name); ok {
			t.Errorf("%s: read as a state", name)
		}
	}
}
