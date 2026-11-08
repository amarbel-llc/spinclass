package rebaserecord

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadClearRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var f Files
	if _, ok := f.Read("/w/a"); ok {
		t.Fatal("a record exists before any write")
	}
	if err := f.Write("/w/a", "refs/heads/feat"); err != nil {
		t.Fatal(err)
	}
	if got, ok := f.Read("/w/a"); !ok || got != "refs/heads/feat" {
		t.Errorf("Read = %q, %v", got, ok)
	}
	if _, ok := f.Read("/w/b"); ok {
		t.Error("a record for /w/a answered for /w/b")
	}
	f.Clear("/w/a")
	if _, ok := f.Read("/w/a"); ok {
		t.Error("the record survived Clear")
	}
	f.Clear("/w/a") // absent is fine
}

func TestWriteRefusesWhatIsNotABranchRef(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, bad := range []string{"", "feat", "refs/tags/v1", "refs/heads/", "refs/heads/a\nb"} {
		if err := (Files{}).Write("/w", bad); err == nil {
			t.Errorf("Write accepted %q", bad)
		}
	}
	if err := (Files{}).Write("", "refs/heads/x"); err == nil {
		t.Error("Write accepted an empty worktree")
	}
}

// A damaged file, or one whose worktree field disagrees with its key, is no
// record.
func TestReadIgnoresADamagedOrMisfiledRecord(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	var f Files
	if err := f.Write("/w/a", "refs/heads/feat"); err != nil {
		t.Fatal(err)
	}
	path := file("/w/a")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Read("/w/a"); ok {
		t.Error("a damaged record was read")
	}
	if err := os.WriteFile(path, []byte(`{"worktree":"/w/other","head":"refs/heads/feat"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Read("/w/a"); ok {
		t.Error("a record naming another worktree was read")
	}
}

func TestDirDefaultsToHomeLocalState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	if got, want := Dir(), filepath.Join(home, ".local", "state", "spinclass", "rebases"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}
