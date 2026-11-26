package sweatfileio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSwitchRaw(t *testing.T, home, body string) string {
	t.Helper()
	path := KeyholderSwitchPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKeyholderSwitchPathIsASiblingOfTheHostsSweatfile(t *testing.T) {
	if got, want := KeyholderSwitchPath("/home/u"), "/home/u/.config/spinclass/keyholder-switch"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestAnAbsentSwitchFileIsDefaultAndNotPresent(t *testing.T) {
	v, present, err := ReadKeyholderSwitch(t.TempDir())
	if err != nil || present || v != KeyholderSwitchDefault {
		t.Errorf("absent = (%q, %v, %v), want (default, false, nil)", v, present, err)
	}
}

func TestTheSwitchFileHoldsOneOfThreeWordsWithANewlineTolerated(t *testing.T) {
	for _, tc := range []struct {
		body string
		want KeyholderSwitchValue
	}{
		{"on\n", KeyholderSwitchOn}, {"off", KeyholderSwitchOff}, {"  default \n", KeyholderSwitchDefault},
	} {
		home := t.TempDir()
		writeSwitchRaw(t, home, tc.body)
		v, present, err := ReadKeyholderSwitch(home)
		if err != nil || !present || v != tc.want {
			t.Errorf("%q = (%q, %v, %v), want (%q, true, nil)", tc.body, v, present, err, tc.want)
		}
	}
}

func TestAGarbageOrLongSwitchFileIsAnErrorNamingThePathAndTheWayOut(t *testing.T) {
	for name, body := range map[string]string{
		"garbage": "maybe\n", "two words": "on off\n", "empty": "", "wrong case": "ON\n",
		"long": strings.Repeat("on\n", 100),
	} {
		home := t.TempDir()
		path := writeSwitchRaw(t, home, body)
		_, present, err := ReadKeyholderSwitch(home)
		if err == nil || !present {
			t.Errorf("%s: (%v, %v), want an error and present", name, present, err)
			continue
		}
		if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "sc keyholder-switch default") || !strings.Contains(err.Error(), "remove the file") {
			t.Errorf("%s: error %q does not name the path and the way out", name, err)
		}
	}
}

func TestASwitchFileThatIsALinkOrADirectoryIsNeverFollowed(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := KeyholderSwitchPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if v, present, err := ReadKeyholderSwitch(home); err == nil || !present {
		t.Errorf("a link read as (%q, %v, %v), want an error", v, present, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadKeyholderSwitch(home); err == nil {
		t.Error("a directory read as a switch file")
	}
}

func TestAnUnreadableSwitchFileThatExistsRefuses(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a mode-0 file")
	}
	home := t.TempDir()
	path := writeSwitchRaw(t, home, "off\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, present, err := ReadKeyholderSwitch(home); err == nil || !present {
		t.Errorf("an unreadable file that exists = (%v, %v), want an error", present, err)
	}
}

func TestARelativeHomeCannotLocateTheSwitchFile(t *testing.T) {
	if _, _, err := ReadKeyholderSwitch("relative/home"); err == nil {
		t.Error("a relative home was accepted")
	}
	if err := WriteKeyholderSwitch("relative/home", KeyholderSwitchOn); err == nil {
		t.Error("a relative home was accepted for a write")
	}
}

func TestWritingTheSwitchFileIsAtomicOwnerOnlyAndLeavesNoTemporary(t *testing.T) {
	home := t.TempDir()
	for _, v := range []KeyholderSwitchValue{KeyholderSwitchOn, KeyholderSwitchOff, KeyholderSwitchDefault} {
		if err := WriteKeyholderSwitch(home, v); err != nil {
			t.Fatal(err)
		}
		got, present, err := ReadKeyholderSwitch(home)
		if err != nil || !present || got != v {
			t.Fatalf("after writing %q read (%q, %v, %v)", v, got, present, err)
		}
	}
	path := KeyholderSwitchPath(home)
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("switch file mode = %v %v, want 0600", fi, err)
	}
	if di, err := os.Stat(filepath.Dir(path)); err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("created directory mode = %v %v, want 0700", di, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("the directory holds %d entries, want only the switch file: %v", len(entries), entries)
	}
}

func TestWritingRefusesAnUnsafeDirectoryOrTarget(t *testing.T) {
	// A directory writable by group or other.
	home := t.TempDir()
	dir := filepath.Dir(KeyholderSwitchPath(home))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeyholderSwitch(home, KeyholderSwitchOn); err == nil || !strings.Contains(err.Error(), "writable by group or other") {
		t.Errorf("group-writable directory = %v, want a refusal", err)
	}

	// A symlinked directory.
	home2 := t.TempDir()
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home2, ".config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(home2, ".config", "spinclass")); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeyholderSwitch(home2, KeyholderSwitchOn); err == nil {
		t.Error("a symlinked directory was written through")
	}
	if entries, _ := os.ReadDir(real); len(entries) != 0 {
		t.Errorf("something was written through the link: %v", entries)
	}

	// A target that is a link: refused, and what it points at is untouched.
	home3 := t.TempDir()
	path := KeyholderSwitchPath(home3)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeyholderSwitch(home3, KeyholderSwitchOff); err == nil {
		t.Error("a link target was replaced")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep\n" {
		t.Errorf("the link's target was changed: %q", b)
	}
}

func TestWritingRefusesAValueThatIsNotOneOfTheThreeWords(t *testing.T) {
	home := t.TempDir()
	if err := WriteKeyholderSwitch(home, KeyholderSwitchValue("maybe")); err == nil {
		t.Error("a bad value was written")
	}
	if _, err := os.Stat(KeyholderSwitchPath(home)); err == nil {
		t.Error("a refused write left a file")
	}
}
