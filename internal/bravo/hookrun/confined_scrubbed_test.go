package hookrun_test

import (
	"path/filepath"
	"strings"
	"testing"

	. "code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

// The regenerate phase runs from a frontend that sits inside the session
// worktree's direnv. The strict gate refuses that; the scrubbed variant
// removes the worktree's PATH entries and ignores DIRENV_*, so the operator
// does nothing, and still never uses a tool from under the forbidden dirs.
func TestConfinedGateEnvScrubbedWorksFromInsideTheWorktreesDirenv(t *testing.T) {
	defer SetConfinedGateTools("git", "sh")()
	wt := t.TempDir()
	poisoned := filepath.Join(wt, ".direnv", "bin")
	writeTool(t, poisoned, "git") // a planted git, first on PATH
	writeTool(t, poisoned, "sh")
	trusted := filepath.Join(t.TempDir(), "trusted", "bin")
	trustedGit := writeTool(t, trusted, "git")
	writeTool(t, trusted, "sh")
	t.Setenv("PATH", poisoned+":"+trusted)
	t.Setenv("DIRENV_DIR", "-"+wt)
	t.Setenv("DIRENV_FILE", filepath.Join(wt, ".envrc"))

	if _, err := ConfinedGateEnv(t.TempDir(), []string{wt}); err == nil {
		t.Fatal("the strict gate must still refuse a frontend inside the worktree's direnv")
	}
	env, err := ConfinedGateEnvScrubbed(t.TempDir(), []string{wt})
	if err != nil {
		t.Fatalf("ConfinedGateEnvScrubbed: %v", err)
	}
	p := pathOf(env)
	if strings.Contains(p, wt) {
		t.Errorf("the gate PATH still names the worktree: %s", p)
	}
	if !strings.Contains(p, filepath.Dir(trustedGit)) {
		t.Errorf("the gate PATH %q lacks the trusted tool directory", p)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "DIRENV_") {
			t.Errorf("a DIRENV variable reached the gate env: %s", kv)
		}
	}
}

// With every copy of a tool under the forbidden dirs, the scrubbed variant
// refuses rather than reaching for one.
func TestConfinedGateEnvScrubbedRefusesAToolFoundOnlyUnderTheWorkerTree(t *testing.T) {
	defer SetConfinedGateTools("git", "sh")()
	wt := t.TempDir()
	poisoned := filepath.Join(wt, ".direnv", "bin")
	writeTool(t, poisoned, "git")
	writeTool(t, poisoned, "sh")
	t.Setenv("PATH", poisoned)

	_, err := ConfinedGateEnvScrubbed(t.TempDir(), []string{wt})
	if err == nil || !strings.Contains(err.Error(), "not on the frontend's PATH") {
		t.Fatalf("err = %v, want the tool reported missing once the worktree entries are removed", err)
	}
}

// A link-time pin under the worker tree is refused by the scrubbed variant too:
// scrubbing PATH does not exempt a pin.
func TestConfinedGateEnvScrubbedStillRefusesAPinUnderTheWorkerTree(t *testing.T) {
	defer SetConfinedGateTools("git", "sh")()
	wt := t.TempDir()
	git := writeTool(t, filepath.Join(wt, "result", "bin"), "git")
	sh := writeTool(t, filepath.Join(t.TempDir(), "pins"), "sh")
	pinGateTools(t, "", git, sh)
	t.Setenv("PATH", t.TempDir())

	if _, err := ConfinedGateEnvScrubbed(t.TempDir(), []string{wt}); err == nil || !strings.Contains(err.Error(), wt) {
		t.Fatalf("err = %v, want a refusal naming the worktree", err)
	}
}

func TestResolveGateToolScrubbedSkipsForbiddenPathEntries(t *testing.T) {
	wt := t.TempDir()
	poisoned := filepath.Join(wt, "bin")
	writeTool(t, poisoned, "systemd-run")
	trusted := filepath.Join(t.TempDir(), "trusted")
	want := writeTool(t, trusted, "systemd-run")
	t.Setenv("PATH", poisoned+":"+trusted)

	got, err := ResolveGateToolScrubbed("systemd-run", []string{wt})
	if err != nil {
		t.Fatalf("ResolveGateToolScrubbed: %v", err)
	}
	if resolved, _ := filepath.EvalSymlinks(want); got != resolved && got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
