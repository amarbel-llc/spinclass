package hookrun_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	. "code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

// A guarded merge's gate (spinclass#449) runs the trusted command in the build
// worktree with nothing the worker controls reaching it: no direnv (so no
// .envrc, no devshell, no `dotenv .spinclass/env`), none of the repo's own
// pre-merge command, and an environment built from scratch rather than
// inherited, with the nix evaluator's escape hatches pinned shut.
func TestConfinedPreMergeNeverLoadsDirenvAndScrubsTheEnvironment(t *testing.T) {
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	// The test sandbox need not have nix; git and sh it has.
	restore := SetConfinedGateTools("git", "sh")
	defer restore()

	runDir := t.TempDir()
	markers := t.TempDir()
	direnvMarker := filepath.Join(markers, "direnv-ran")
	preMergeMarker := filepath.Join(markers, "pre-merge-ran")
	envOut := filepath.Join(markers, "env")

	pinDirenv(t, "#!/bin/sh\n: > "+direnvMarker+"\nshift 2\nexec \"$@\"\n")
	// A build worktree carries whatever .envrc the branch committed; the
	// normal path would wrap the hook in `direnv exec` for it.
	if err := os.WriteFile(filepath.Join(runDir, ".envrc"), []byte("use flake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"SSH_AUTH_SOCK":        "/run/user/1/agent",
		"NIX_CONFIG":           "sandbox = false",
		"NIX_PATH":             "nixpkgs=/x",
		"BASH_ENV":             "/x/planted",
		"ENV":                  "/x/planted",
		"DIRENV_DIR":           "-/x/elsewhere",
		"GIT_DIR":              "/x/.git",
		"GIT_SSH_COMMAND":      "ssh -i /x",
		"CLOWN_SESSION_ID":     "c",
		"SPINCLASS_SESSION_ID": "s",
		"TMPDIR":               runDir,
		"CLAUDE_CODE_TMPDIR":   runDir,
		"LD_PRELOAD":           "/x/evil.so",
		"XDG_RUNTIME_DIR":      "/run/user/1",
	} {
		t.Setenv(k, v)
	}
	t.Setenv("HOME", t.TempDir())

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr(": > " + preMergeMarker)}}
	var out bytes.Buffer
	err := ConfinedPreMergeInDir(context.Background(), sf, "export -p > "+envOut+"; pwd >> "+envOut, runDir, []string{t.TempDir()}, &out)
	if err != nil {
		t.Fatalf("confined gate: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(direnvMarker); err == nil {
		t.Error("direnv ran: the guarded gate must never load the devshell")
	}
	if _, err := os.Stat(preMergeMarker); err == nil {
		t.Error("the repo's pre-merge command ran: the guarded gate replaces it")
	}
	raw, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if got := lines[len(lines)-1]; got != runDir {
		t.Errorf("cwd = %q, want the build worktree %q", got, runDir)
	}
	env := strings.Join(lines[:len(lines)-1], "\n")
	for _, k := range []string{
		"SSH_AUTH_SOCK", "NIX_PATH", "BASH_ENV", "ENV", "DIRENV_DIR", "GIT_DIR", "GIT_SSH_COMMAND",
		"CLOWN_SESSION_ID", "SPINCLASS_SESSION_ID", "TMPDIR", "CLAUDE_CODE_TMPDIR", "LD_PRELOAD",
	} {
		if strings.Contains(env, "export "+k+"=") {
			t.Errorf("%s reached the gate; the environment must be built from scratch", k)
		}
	}
	for _, want := range []string{
		"GIT_ALLOW_PROTOCOL=\"file:https\"",
		"WORKTREE=\"" + runDir + "\"",
		"SPINCLASS_GUARDED_GATE=\"1\"",
		"export HOME=",
		"XDG_RUNTIME_DIR=\"/run/user/1\"",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %s in the gate's environment:\n%s", want, env)
		}
	}
	if !strings.Contains(env, "export NIX_CONFIG=") || strings.Contains(env, "sandbox = false") {
		t.Errorf("NIX_CONFIG must be spinclass's pinned options, not the inherited value:\n%s", env)
	}
	for _, opt := range []string{
		"allow-unsafe-native-code-during-evaluation = false",
		"accept-flake-config = false",
		"sandbox = true",
	} {
		if !strings.Contains(ConfinedGateNixConfig, opt) {
			t.Errorf("pinned NIX_CONFIG lacks %q", opt)
		}
	}
	pathLine := ""
	for _, l := range lines {
		if strings.HasPrefix(l, "export PATH=") {
			pathLine = l
		}
	}
	if pathLine == "" {
		t.Fatalf("no PATH in the gate's environment:\n%s", env)
	}
	if strings.Count(pathLine, ":") > 2 {
		t.Errorf("PATH is not the few tool directories: %s", pathLine)
	}
}

// A gate that cannot even find its tools fails closed, before anything runs.
func TestConfinedPreMergeRefusesWhenAToolIsMissing(t *testing.T) {
	restore := SetConfinedGateTools("nix-does-not-exist-here", "sh")
	defer restore()
	marker := filepath.Join(t.TempDir(), "ran")
	err := ConfinedPreMergeInDir(context.Background(), sweatfile.Sweatfile{}, ": > "+marker, t.TempDir(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "nix-does-not-exist-here") {
		t.Fatalf("err = %v, want a refusal naming the missing tool", err)
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Error("the command ran although a tool was missing")
	}
}

// The frontend's PATH is not pinned yet (TODO(#454)): a tool that resolves
// under a directory the worker writes, or a frontend running inside such a
// directory's direnv, is refused rather than trusted.
func TestConfinedPreMergeRefusesToolsFromTheWorkerTree(t *testing.T) {
	restore := SetConfinedGateTools("git", "sh")
	defer restore()
	wt := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")

	t.Run("tool under the session worktree", func(t *testing.T) {
		bin := filepath.Join(wt, "result", "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
		err := ConfinedPreMergeInDir(context.Background(), sweatfile.Sweatfile{}, ": > "+marker, t.TempDir(), []string{wt}, nil)
		if err == nil || !strings.Contains(err.Error(), "git") || !strings.Contains(err.Error(), wt) {
			t.Fatalf("err = %v, want a refusal naming git under the worktree", err)
		}
	})
	t.Run("frontend inside the worktree's direnv", func(t *testing.T) {
		t.Setenv("DIRENV_DIR", "-"+wt)
		err := ConfinedPreMergeInDir(context.Background(), sweatfile.Sweatfile{}, ": > "+marker, t.TempDir(), []string{wt}, nil)
		if err == nil || !strings.Contains(err.Error(), "direnv") {
			t.Fatalf("err = %v, want a refusal naming the direnv", err)
		}
	})
	if _, serr := os.Stat(marker); serr == nil {
		t.Error("the command ran although the gate was refused")
	}
}

// The inactivity watchdog guards the confined gate as it does the normal one.
func TestConfinedPreMergeHonoursInactivityTimeout(t *testing.T) {
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	restore := SetConfinedGateTools("git", "sh")
	defer restore()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{InactivityTimeout: sptr("300ms")}}
	// The scrubbed PATH has no coreutils, so no `sleep`: a silent shell loop
	// is the hang.
	err := ConfinedPreMergeInDir(context.Background(), sf, "while :; do :; done", t.TempDir(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "inactivity-timeout") {
		t.Fatalf("err = %v, want the inactivity kill", err)
	}
}
