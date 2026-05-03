package madder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit_NoBinaryDormant(t *testing.T) {
	worktree := t.TempDir()

	if err := Init(worktree, ""); err != nil {
		t.Fatalf("Init with empty binPath returned err: %v", err)
	}

	madderDir := filepath.Join(worktree, ".madder")
	if _, err := os.Stat(madderDir); !os.IsNotExist(err) {
		t.Errorf("expected no .madder/ directory when binPath is empty, stat err=%v", err)
	}
}

func TestInit_RunsWhenAbsent(t *testing.T) {
	worktree := t.TempDir()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "fake-madder")
	logPath := filepath.Join(binDir, "log")

	// Fake madder script: log argv, then create the marker file madder
	// itself would create on a real init.
	script := `#!/bin/sh
echo "$@" >>"` + logPath + `"
mkdir -p "$PWD/.madder/local/share/blob_stores/default"
touch "$PWD/.madder/local/share/blob_stores/default/blob_store-config"
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake madder: %v", err)
	}

	if err := Init(worktree, binPath); err != nil {
		t.Fatalf("Init: %v", err)
	}

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	want := "init -encryption none .default\n"
	if string(logged) != want {
		t.Errorf("argv: got %q, want %q", logged, want)
	}

	if !StoreReady(worktree) {
		t.Errorf("expected StoreReady=true after Init")
	}
}

func TestInit_IdempotentWhenConfigExists(t *testing.T) {
	worktree := t.TempDir()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "fake-madder")
	logPath := filepath.Join(binDir, "log")

	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho ran >>\""+logPath+"\"\n"), 0o755); err != nil {
		t.Fatalf("writing fake madder: %v", err)
	}

	cfgDir := filepath.Join(worktree, ".madder", "local", "share", "blob_stores", "default")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("pre-creating config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "blob_store-config"), []byte{}, 0o444); err != nil {
		t.Fatalf("pre-creating config file: %v", err)
	}

	if err := Init(worktree, binPath); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("expected fake madder NOT to be invoked when blob_store-config already exists; log stat err=%v", err)
	}
}

func TestLinkInto_NoBinaryDormant(t *testing.T) {
	binDir := t.TempDir()
	if err := LinkInto(binDir, ""); err != nil {
		t.Fatalf("LinkInto with empty binPath returned err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binDir, "madder")); !os.IsNotExist(err) {
		t.Errorf("expected no madder symlink when binPath empty, stat err=%v", err)
	}
}

func TestLinkInto_CreatesSymlink(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "spinclass", "bin")
	target := filepath.Join(t.TempDir(), "real-madder")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := LinkInto(binDir, target); err != nil {
		t.Fatalf("LinkInto: %v", err)
	}

	link := filepath.Join(binDir, "madder")
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if got != target {
		t.Errorf("symlink target: got %q, want %q", got, target)
	}
}

func TestLinkInto_OverwritesExistingLink(t *testing.T) {
	binDir := t.TempDir()
	older := filepath.Join(t.TempDir(), "old-madder")
	newer := filepath.Join(t.TempDir(), "new-madder")
	for _, p := range []string{older, newer} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := LinkInto(binDir, older); err != nil {
		t.Fatalf("first LinkInto: %v", err)
	}
	if err := LinkInto(binDir, newer); err != nil {
		t.Fatalf("second LinkInto: %v", err)
	}

	got, err := os.Readlink(filepath.Join(binDir, "madder"))
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if got != newer {
		t.Errorf("expected symlink to be replaced; got %q, want %q", got, newer)
	}
}

func TestInit_PassesCeilingEnv(t *testing.T) {
	worktree := t.TempDir()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "fake-madder")
	envPath := filepath.Join(binDir, "env")

	script := `#!/bin/sh
env >"` + envPath + `"
mkdir -p "$PWD/.madder/local/share/blob_stores/default"
touch "$PWD/.madder/local/share/blob_stores/default/blob_store-config"
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake madder: %v", err)
	}

	if err := Init(worktree, binPath); err != nil {
		t.Fatalf("Init: %v", err)
	}

	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading env capture: %v", err)
	}
	want := "MADDER_CEILING_DIRECTORIES=" + worktree
	if !strings.Contains(string(envBytes), want) {
		t.Errorf("expected %q in env, got:\n%s", want, envBytes)
	}
}
