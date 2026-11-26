package sweatfileio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// L-5: a path component that is a file names no switch file: a host that never
// used the key holder is not refused for it.
func TestAPathComponentThatIsAFileIsAnAbsentSwitchFile(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "spinclass"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, present, err := ReadKeyholderSwitch(home)
	if err != nil || present || v != KeyholderSwitchDefault {
		t.Errorf("a file where the directory should be = (%q, %v, %v), want absent", v, present, err)
	}
}

// L-7: the reader checks the mode the writer sets: a file group or other can
// write is not the operator's word.
func TestAGroupOrOtherWritableSwitchFileRefuses(t *testing.T) {
	home := t.TempDir()
	path := KeyholderSwitchPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	_, present, err := ReadKeyholderSwitch(home)
	if err == nil || !present || !strings.Contains(err.Error(), "writable by group or other") || !strings.Contains(err.Error(), "chmod go-w") {
		t.Fatalf("a 0666 switch file = (%v, %v), want a refusal naming the mode and the remedy", present, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if v, _, err := ReadKeyholderSwitch(home); err != nil || v != KeyholderSwitchOff {
		t.Errorf("a 0644 switch file = (%q, %v), want it read", v, err)
	}
}

// L-8: a leftover temporary file of an earlier run cannot make the write fail,
// and a directory group can write names its remedy.
func TestAStaleTemporaryFileDoesNotBlockTheWriteAndAnUnsafeDirectoryNamesItsRemedy(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Dir(KeyholderSwitchPath(home))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".keyholder-switch.1.tmp"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeyholderSwitch(home, KeyholderSwitchOff); err != nil {
		t.Fatalf("a stale temporary file blocked the write: %v", err)
	}
	if v, _, err := ReadKeyholderSwitch(home); err != nil || v != KeyholderSwitchOff {
		t.Fatalf("after the write the file reads (%q, %v)", v, err)
	}
	if err := os.Chmod(dir, 0o775); err != nil {
		t.Fatal(err)
	}
	err := WriteKeyholderSwitch(home, KeyholderSwitchOn)
	if err == nil || !strings.Contains(err.Error(), "chmod go-w") {
		t.Errorf("a 0775 directory = %v, want a refusal naming `chmod go-w`", err)
	}
}
