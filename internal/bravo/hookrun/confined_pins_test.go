package hookrun_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/embeds"
	. "code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

// writeTool writes an executable stand-in named tool under dir and returns
// its path.
func writeTool(t *testing.T, dir, tool string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, tool)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// pinGateTools pins the guarded gate's tools for the test and restores the
// unpinned default afterwards.
func pinGateTools(t *testing.T, nix, git, sh string) {
	t.Helper()
	embeds.SetGateTools(nix, git, sh)
	t.Cleanup(func() { embeds.SetGateTools("", "", "") })
}

// A build with the gate's tools pinned at link time (spinclass#454, the way
// direnv and madder are) never consults the frontend's PATH for them: the
// gate's PATH is the pinned tools' directories, found with PATH empty.
func TestConfinedGateUsesThePinnedToolsAndNeverConsultsPATH(t *testing.T) {
	defer SetConfinedGateTools("git", "sh")()
	pins := filepath.Join(t.TempDir(), "pins", "bin")
	git := writeTool(t, pins, "git")
	sh := writeTool(t, pins, "sh")
	pinGateTools(t, "", git, sh)
	t.Setenv("PATH", t.TempDir()) // nothing on it

	env, err := ConfinedGateEnv(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("ConfinedGateEnv with pinned tools and an empty PATH: %v", err)
	}
	if !hasEnv(env, "PATH="+pins) {
		t.Errorf("gate PATH is not the pinned directory alone: %v", pathOf(env))
	}
}

// An unpinned tool (a plain `go build`, or a consumer that pins only some)
// still falls back to the frontend's PATH, tool by tool.
func TestConfinedGateFallsBackToPATHForAnUnpinnedTool(t *testing.T) {
	defer SetConfinedGateTools("git", "sh")()
	pins := filepath.Join(t.TempDir(), "pins", "bin")
	sh := writeTool(t, pins, "sh")
	pinGateTools(t, "", "", sh)
	onPath := filepath.Join(t.TempDir(), "path", "bin")
	writeTool(t, onPath, "git")
	t.Setenv("PATH", onPath)

	env, err := ConfinedGateEnv(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("ConfinedGateEnv with a partial pin: %v", err)
	}
	if p := pathOf(env); !strings.Contains(p, onPath) || !strings.Contains(p, pins) {
		t.Errorf("gate PATH = %q, want both the PATH-resolved git dir %s and the pinned sh dir %s", p, onPath, pins)
	}
}

// A pin that points under a directory the worker writes is refused like a
// PATH-resolved tool would be: the pin is trusted for where it comes from, not
// exempt from the check.
func TestConfinedGateRefusesAPinnedToolUnderTheWorkerTree(t *testing.T) {
	defer SetConfinedGateTools("git", "sh")()
	wt := t.TempDir()
	git := writeTool(t, filepath.Join(wt, "result", "bin"), "git")
	sh := writeTool(t, filepath.Join(t.TempDir(), "pins"), "sh")
	pinGateTools(t, "", git, sh)
	t.Setenv("PATH", t.TempDir())

	if _, err := ConfinedGateEnv(t.TempDir(), []string{wt}); err == nil || !strings.Contains(err.Error(), wt) {
		t.Fatalf("err = %v, want a refusal naming the worktree", err)
	}
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func pathOf(env []string) string {
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			return e
		}
	}
	return ""
}
