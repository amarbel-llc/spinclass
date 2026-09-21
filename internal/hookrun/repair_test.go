package hookrun_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "code.linenisgreat.com/spinclass/internal/hookrun"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

func TestRepairRuns(t *testing.T) {
	wt := t.TempDir()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Repair: sptr("echo repaired")}}
	var buf bytes.Buffer
	if err := Repair(context.Background(), sf, wt, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	if got := strings.TrimSpace(buf.String()); got != "repaired" {
		t.Errorf("expected hook output 'repaired', got %q", got)
	}
}

func TestRepairInactiveIsNoop(t *testing.T) {
	wt := t.TempDir()
	// No repair command → the runner is a no-op even though the writer is wired.
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{}}
	var buf bytes.Buffer
	if err := Repair(context.Background(), sf, wt, &buf); err != nil {
		t.Fatalf("inactive repair should be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("inactive repair wrote output: %q", buf.String())
	}
	// Disabled-but-set is also a no-op.
	sf = sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Repair: sptr("echo nope"), DisableRepair: bptr(true)}}
	buf.Reset()
	if err := Repair(context.Background(), sf, wt, &buf); err != nil {
		t.Fatalf("disabled repair should be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("disabled repair ran: %q", buf.String())
	}
}

func TestRepairPropagatesFailure(t *testing.T) {
	wt := t.TempDir()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Repair: sptr("echo boom >&2; exit 1")}}
	var buf bytes.Buffer
	if err := Repair(context.Background(), sf, wt, &buf); err == nil {
		t.Fatalf("expected nonzero repair to error, got nil (output: %q)", buf.String())
	}
}

// When the worktree has a .envrc and direnv resolves, the hook must run inside
// the worktree devshell via `direnv exec <wt> sh -c <script>` — so a
// devShell-provided hook command resolves even when the spinclass process is
// not itself inside that devShell (spinclass#198). The fake direnv proves it
// was the entrypoint (sentinel line) and that it forwarded to the real script
// (the script's own output still appears).
func TestRepairWrapsWithDirenvWhenEnvrcPresent(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".envrc"), []byte("use flake\n"), 0o644); err != nil {
		t.Fatalf("writing .envrc: %v", err)
	}

	// fake direnv: `direnv exec <dir> <cmd...>` → emit a sentinel, drop the
	// `exec <dir>` argv, then run the remaining command so the wrapped script
	// still executes.
	pinDirenv(t, "#!/bin/sh\necho DIRENV_WRAPPED\nshift 2\nexec \"$@\"\n")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Repair: sptr("echo repaired")}}
	var buf bytes.Buffer
	if err := Repair(context.Background(), sf, wt, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "DIRENV_WRAPPED") {
		t.Errorf("expected hook to run via direnv exec, got %q", out)
	}
	if !strings.Contains(out, "repaired") {
		t.Errorf("expected wrapped script output 'repaired', got %q", out)
	}
}

// Without a .envrc the hook runs as a bare `sh -c`, never touching direnv, even
// when a direnv binary is on PATH. Guards the non-direnv-repo behavior.
func TestRepairNoEnvrcSkipsDirenv(t *testing.T) {
	wt := t.TempDir()

	// If this fake is ever invoked the test fails: it emits the sentinel but
	// does NOT forward, so the script's own output would be missing.
	pinDirenv(t, "#!/bin/sh\necho DIRENV_WRAPPED\n")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Repair: sptr("echo repaired")}}
	var buf bytes.Buffer
	if err := Repair(context.Background(), sf, wt, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	out := buf.String()
	if strings.Contains(out, "DIRENV_WRAPPED") {
		t.Errorf("expected bare sh -c (no direnv) without .envrc, got %q", out)
	}
	if strings.TrimSpace(out) != "repaired" {
		t.Errorf("expected hook output 'repaired', got %q", out)
	}
}

// The pre-merge hook runs in a detached build worktree (runDir) that lacks the
// git-excluded .envrc, but must load the devshell from the session worktree
// (envDir), which has an allowed .envrc. PreMergeInDir must therefore gate on
// and `direnv exec` envDir while leaving cmd.Dir at runDir (spinclass#198).
// The fake direnv echoes its dir argument so the test can assert the devshell
// came from envDir, and the script prints $PWD so the test can assert the hook
// ran in runDir.
func TestPreMergeInDirLoadsDevshellFromEnvDir(t *testing.T) {
	envDir := t.TempDir() // session worktree: has .envrc
	runDir := t.TempDir() // build worktree: no .envrc
	if err := os.WriteFile(filepath.Join(envDir, ".envrc"), []byte("use flake\n"), 0o644); err != nil {
		t.Fatalf("writing .envrc: %v", err)
	}

	// fake direnv: `direnv exec <dir> <cmd...>` → report the dir it was asked to
	// load, drop the `exec <dir>` argv, then run the remaining command.
	pinDirenv(t, "#!/bin/sh\necho DIRENV_DIR=$2\nshift 2\nexec \"$@\"\n")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr("echo PWD=$PWD")}}
	var buf bytes.Buffer
	if err := PreMergeInDir(context.Background(), sf, envDir, runDir, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "DIRENV_DIR="+envDir) {
		t.Errorf("expected devshell loaded from envDir %q, got %q", envDir, out)
	}
	if !strings.Contains(out, "PWD="+runDir) {
		t.Errorf("expected hook cwd to be runDir %q, got %q", runDir, out)
	}
}

// When runDir lacks a .envrc but envDir has one (the build-worktree case), the
// gate keys off envDir, not runDir — guarding against a regression where the
// gate is mistakenly checked against the run directory and silently drops the
// devshell wrap on the default merge path.
func TestPreMergeInDirGatesOnEnvDirNotRunDir(t *testing.T) {
	envDir := t.TempDir()
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(envDir, ".envrc"), []byte("use flake\n"), 0o644); err != nil {
		t.Fatalf("writing .envrc: %v", err)
	}

	pinDirenv(t, "#!/bin/sh\necho DIRENV_WRAPPED\nshift 2\nexec \"$@\"\n")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr("echo verified")}}
	var buf bytes.Buffer
	if err := PreMergeInDir(context.Background(), sf, envDir, runDir, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "DIRENV_WRAPPED") {
		t.Errorf("expected devshell wrap gated on envDir's .envrc, got %q", buf.String())
	}
}
