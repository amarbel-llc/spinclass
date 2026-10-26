package madder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// userStoreConfig is where madder(1) FILES puts an unprefixed store id.
func userStoreConfig(dataHome string) string {
	return filepath.Join(dataHome, "madder", "blob_stores", UserStoreID, "blob_store-config")
}

func TestUserStoreIDIsSpinclass(t *testing.T) {
	if UserStoreID != "spinclass" {
		t.Fatalf("UserStoreID = %q", UserStoreID)
	}
}

func TestUserStoreReady_FollowsXDGDataHome(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	if UserStoreReady() {
		t.Fatal("ready before init")
	}
	cfg := userStoreConfig(data)
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if !UserStoreReady() {
		t.Fatal("not ready with blob_store-config present")
	}
}

func TestUserStoreReady_DefaultsToHomeLocalShare(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", home)
	cfg := userStoreConfig(filepath.Join(home, ".local", "share"))
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if !UserStoreReady() {
		t.Fatal("not ready under $HOME/.local/share")
	}
}

func TestInitUserStore_NoBinaryDormant(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	if err := InitUserStore(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "madder")); !os.IsNotExist(err) {
		t.Errorf("created %s with no binary (stat err %v)", data, err)
	}
}

func TestInitUserStore_RunsOnceWithScopedEnv(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("MADDER_XDG_USER_LOCATION_ONLY", "")
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "fake-madder")
	logPath := filepath.Join(binDir, "log")
	script := `#!/bin/sh
echo "$@ LOCONLY=$MADDER_XDG_USER_LOCATION_ONLY" >>"` + logPath + `"
mkdir -p "$XDG_DATA_HOME/madder/blob_stores/spinclass"
touch "$XDG_DATA_HOME/madder/blob_stores/spinclass/blob_store-config"
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := InitUserStore(binPath); err != nil {
			t.Fatalf("InitUserStore #%d: %v", i, err)
		}
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "init -encryption none spinclass LOCONLY=1\n"
	if string(logged) != want {
		t.Errorf("invocations: got %q, want exactly one %q", logged, want)
	}
	if v := os.Getenv("MADDER_XDG_USER_LOCATION_ONLY"); v != "" {
		t.Errorf("MADDER_XDG_USER_LOCATION_ONLY leaked into the process env: %q", v)
	}
}

func TestInitUserStore_ReportsFailure(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	binPath := filepath.Join(t.TempDir(), "fake-madder")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho boom 1>&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := InitUserStore(binPath)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want madder stderr", err)
	}
}

func TestWriteUserStore_NoBinaryDormant(t *testing.T) {
	w, finish, err := WriteUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("x"))
	_ = w.Close()
	if id, err := finish(); id != "" || err != nil {
		t.Errorf("finish = (%q, %v)", id, err)
	}
}

func TestWriteUserStore_AddressesTheUserStoreFromANeutralDir(t *testing.T) {
	t.Setenv("MADDER_XDG_USER_LOCATION_ONLY", "")
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "fake-madder")
	capture := filepath.Join(binDir, "capture")
	stdinPath := filepath.Join(binDir, "stdin")
	script := `#!/bin/sh
echo "$@" >"` + capture + `"
echo "LOCONLY=$MADDER_XDG_USER_LOCATION_ONLY" >>"` + capture + `"
echo "PWD=$PWD" >>"` + capture + `"
cat >"` + stdinPath + `"
printf '{"id":"blake2b256-u","size":2,"source":"-"}\n'
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	w, finish, err := WriteUserStore(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	id, err := finish()
	if err != nil || id != "blake2b256-u" {
		t.Fatalf("finish = (%q, %v)", id, err)
	}

	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	if lines[0] != "write -format json spinclass -" {
		t.Errorf("argv = %q", lines[0])
	}
	if lines[1] != "LOCONLY=1" {
		t.Errorf("env = %q", lines[1])
	}
	cwd, _ := filepath.EvalSymlinks(strings.TrimPrefix(lines[2], "PWD="))
	tmp, _ := filepath.EvalSymlinks(os.TempDir())
	if cwd != tmp {
		t.Errorf("cwd = %q, want the neutral %q", cwd, tmp)
	}
	if b, _ := os.ReadFile(stdinPath); string(b) != "hi" {
		t.Errorf("stdin = %q", b)
	}
}
