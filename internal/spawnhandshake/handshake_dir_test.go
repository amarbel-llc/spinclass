package spawnhandshake

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A confined worker writes its hello where the environment says, and only the
// driver reading that same directory sees it.
func TestSendHelloHonoursTheHandshakeDirEnv(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	own := filepath.Join(t.TempDir(), "session-abc")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DirEnv, own)

	if err := SendHello("repo/feat", "driver-principal", "posh-id"); err != nil {
		t.Fatal(err)
	}
	// Not in the shared dir...
	if _, err := os.Stat(SharedDir()); err == nil {
		t.Errorf("the shared dir %s was created", SharedDir())
	}
	// ...but in the named one, where the driver reads it.
	id, err := WaitForHelloIn(own, "driver-principal", "repo/feat", time.Now().Add(-time.Minute), time.Second)
	if err != nil || id != "posh-id" {
		t.Fatalf("got (%q, %v), want the hello from the session dir", id, err)
	}
	// The shared-dir waiter must NOT see it.
	if _, err := WaitForHello("driver-principal", "repo/feat", time.Now().Add(-time.Minute), 100*time.Millisecond); err == nil {
		t.Error("the shared dir waiter saw a hello written to a session dir")
	}
}

func TestSessionDirIsAPerSessionSubdirOfTheSharedOne(t *testing.T) {
	a := SessionDir("/state", "/w/repo/.worktrees/a")
	b := SessionDir("/state", "/w/repo/.worktrees/b")
	if a == b {
		t.Fatal("two worktrees share a handshake dir")
	}
	if a != SessionDir("/state", "/w/repo/.worktrees/a/") {
		t.Error("the dir is not a function of the cleaned worktree path")
	}
	if !strings.HasPrefix(a, filepath.Join("/state", "spinclass", "spawn-handshake")+"/session-") {
		t.Errorf("unexpected layout: %s", a)
	}
}

func TestSendHelloWithoutTheEnvUsesTheSharedDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(DirEnv, "")
	if err := SendHello("repo/feat", "drv", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := WaitForHello("drv", "repo/feat", time.Now().Add(-time.Minute), time.Second); err != nil {
		t.Errorf("shared path broken: %v", err)
	}
}
