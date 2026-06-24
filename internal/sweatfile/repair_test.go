package sweatfile_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/embeds"
	. "github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/sweatfileio"
)

// pinDirenv writes an executable fake direnv with the given shell body and pins
// it as the build-time direnv via embeds.Set, restoring the prior pin on
// cleanup. Pinning (rather than relying on PATH) sidesteps direnv.Resolve's
// embeds-over-PATH precedence so the fake is used deterministically.
func pinDirenv(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "direnv")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing fake direnv: %v", err)
	}
	prevMadder, prevDirenv, prevDodder := embeds.MadderBin(), embeds.DirenvBin(), embeds.DodderBin()
	embeds.Set(prevMadder, path, prevDodder)
	t.Cleanup(func() { embeds.Set(prevMadder, prevDirenv, prevDodder) })
}

func bptr(b bool) *bool { return &b }

func TestParseHooksRepair(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte(
		"[hooks]\nrepair = \"conformist --commit --amend --exit-zero-on-fix\"\ndisable-repair = true\n",
	))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks == nil || sf.Hooks.Repair == nil ||
		*sf.Hooks.Repair != "conformist --commit --amend --exit-zero-on-fix" {
		t.Fatalf("hooks.repair: got %+v", sf.Hooks)
	}
	if sf.Hooks.DisableRepair == nil || !*sf.Hooks.DisableRepair {
		t.Fatalf("hooks.disable-repair: got %+v", sf.Hooks)
	}
	// The regenerated tommy decoder must consume both keys, else `sc validate`
	// would flag them as unknown.
	if u := doc.Undecoded(); len(u) != 0 {
		t.Errorf("repair/disable-repair left undecoded: %v", u)
	}
}

func TestRepairActive(t *testing.T) {
	cases := []struct {
		name    string
		repair  *string
		disable *bool
		want    bool
	}{
		{"nil-hooks-field", nil, nil, false},
		{"empty", sptr(""), nil, false},
		{"whitespace-only", sptr("   \n\t"), nil, false},
		{"set", sptr("conformist --commit --amend"), nil, true},
		{"set-but-disabled", sptr("conformist --commit --amend"), bptr(true), false},
		{"set-disable-false", sptr("conformist --commit --amend"), bptr(false), true},
	}
	for _, c := range cases {
		sf := Sweatfile{Hooks: &Hooks{Repair: c.repair, DisableRepair: c.disable}}
		if got := sf.RepairActive(); got != c.want {
			t.Errorf("%s: RepairActive() = %v, want %v", c.name, got, c.want)
		}
	}
	// No Hooks table at all → inactive.
	if (Sweatfile{}).RepairActive() {
		t.Error("nil Hooks: RepairActive() = true, want false")
	}
}

func TestRepairDisabled(t *testing.T) {
	if (Sweatfile{}).RepairDisabled() {
		t.Error("nil Hooks: RepairDisabled() = true, want false")
	}
	if (Sweatfile{Hooks: &Hooks{}}).RepairDisabled() {
		t.Error("nil DisableRepair: RepairDisabled() = true, want false")
	}
	if !(Sweatfile{Hooks: &Hooks{DisableRepair: bptr(true)}}).RepairDisabled() {
		t.Error("DisableRepair=true: RepairDisabled() = false, want true")
	}
}

func TestMergeHooksRepairOverride(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{Repair: sptr("fmt-a")}}
	repo := Sweatfile{Hooks: &Hooks{Repair: sptr("fmt-b")}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.Repair == nil || *merged.Hooks.Repair != "fmt-b" {
		t.Errorf("expected override fmt-b, got %+v", merged.Hooks)
	}
}

func TestMergeHooksRepairInherit(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{Repair: sptr("fmt-a")}}
	merged := base.MergeWith(Sweatfile{})
	if merged.Hooks == nil || merged.Hooks.Repair == nil || *merged.Hooks.Repair != "fmt-a" {
		t.Errorf("expected inherited fmt-a, got %+v", merged.Hooks)
	}
}

func TestMergeHooksDisableRepairOverride(t *testing.T) {
	// A child sweatfile can suppress an inherited repair command without
	// clearing the string.
	base := Sweatfile{Hooks: &Hooks{Repair: sptr("fmt-a")}}
	repo := Sweatfile{Hooks: &Hooks{DisableRepair: bptr(true)}}
	merged := base.MergeWith(repo)
	if merged.RepairActive() {
		t.Errorf("expected repair suppressed by inherited disable-repair, got active; hooks=%+v", merged.Hooks)
	}
	// The command string is still inherited (suppression, not clearing).
	if merged.Hooks.Repair == nil || *merged.Hooks.Repair != "fmt-a" {
		t.Errorf("expected repair command still inherited, got %+v", merged.Hooks)
	}
}

func TestRunRepairHookContextRuns(t *testing.T) {
	wt := t.TempDir()
	sf := Sweatfile{Hooks: &Hooks{Repair: sptr("echo repaired")}}
	var buf bytes.Buffer
	if err := sf.RunRepairHookContext(context.Background(), wt, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	if got := strings.TrimSpace(buf.String()); got != "repaired" {
		t.Errorf("expected hook output 'repaired', got %q", got)
	}
}

func TestRunRepairHookContextInactiveIsNoop(t *testing.T) {
	wt := t.TempDir()
	// No repair command → the runner is a no-op even though the writer is wired.
	sf := Sweatfile{Hooks: &Hooks{}}
	var buf bytes.Buffer
	if err := sf.RunRepairHookContext(context.Background(), wt, &buf); err != nil {
		t.Fatalf("inactive repair should be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("inactive repair wrote output: %q", buf.String())
	}
	// Disabled-but-set is also a no-op.
	sf = Sweatfile{Hooks: &Hooks{Repair: sptr("echo nope"), DisableRepair: bptr(true)}}
	buf.Reset()
	if err := sf.RunRepairHookContext(context.Background(), wt, &buf); err != nil {
		t.Fatalf("disabled repair should be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("disabled repair ran: %q", buf.String())
	}
}

func TestRunRepairHookContextPropagatesFailure(t *testing.T) {
	wt := t.TempDir()
	sf := Sweatfile{Hooks: &Hooks{Repair: sptr("echo boom >&2; exit 1")}}
	var buf bytes.Buffer
	if err := sf.RunRepairHookContext(context.Background(), wt, &buf); err == nil {
		t.Fatalf("expected nonzero repair to error, got nil (output: %q)", buf.String())
	}
}

// When the worktree has a .envrc and direnv resolves, the hook must run inside
// the worktree devshell via `direnv exec <wt> sh -c <script>` — so a
// devShell-provided hook command resolves even when the spinclass process is
// not itself inside that devShell (spinclass#198). The fake direnv proves it
// was the entrypoint (sentinel line) and that it forwarded to the real script
// (the script's own output still appears).
func TestRunRepairHookContextWrapsWithDirenvWhenEnvrcPresent(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".envrc"), []byte("use flake\n"), 0o644); err != nil {
		t.Fatalf("writing .envrc: %v", err)
	}

	// fake direnv: `direnv exec <dir> <cmd...>` → emit a sentinel, drop the
	// `exec <dir>` argv, then run the remaining command so the wrapped script
	// still executes.
	pinDirenv(t, "#!/bin/sh\necho DIRENV_WRAPPED\nshift 2\nexec \"$@\"\n")

	sf := Sweatfile{Hooks: &Hooks{Repair: sptr("echo repaired")}}
	var buf bytes.Buffer
	if err := sf.RunRepairHookContext(context.Background(), wt, &buf); err != nil {
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
func TestRunRepairHookContextNoEnvrcSkipsDirenv(t *testing.T) {
	wt := t.TempDir()

	// If this fake is ever invoked the test fails: it emits the sentinel but
	// does NOT forward, so the script's own output would be missing.
	pinDirenv(t, "#!/bin/sh\necho DIRENV_WRAPPED\n")

	sf := Sweatfile{Hooks: &Hooks{Repair: sptr("echo repaired")}}
	var buf bytes.Buffer
	if err := sf.RunRepairHookContext(context.Background(), wt, &buf); err != nil {
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
// (envDir), which has an allowed .envrc. RunPreMergeHookInDir must therefore
// gate on and `direnv exec` envDir while leaving cmd.Dir at runDir
// (spinclass#198). The fake direnv echoes its dir argument so the test can
// assert the devshell came from envDir, and the script prints $PWD so the test
// can assert the hook ran in runDir.
func TestRunPreMergeHookInDirLoadsDevshellFromEnvDir(t *testing.T) {
	envDir := t.TempDir() // session worktree: has .envrc
	runDir := t.TempDir() // build worktree: no .envrc
	if err := os.WriteFile(filepath.Join(envDir, ".envrc"), []byte("use flake\n"), 0o644); err != nil {
		t.Fatalf("writing .envrc: %v", err)
	}

	// fake direnv: `direnv exec <dir> <cmd...>` → report the dir it was asked to
	// load, drop the `exec <dir>` argv, then run the remaining command.
	pinDirenv(t, "#!/bin/sh\necho DIRENV_DIR=$2\nshift 2\nexec \"$@\"\n")

	sf := Sweatfile{Hooks: &Hooks{PreMerge: sptr("echo PWD=$PWD")}}
	var buf bytes.Buffer
	if err := sf.RunPreMergeHookInDir(context.Background(), envDir, runDir, &buf); err != nil {
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
func TestRunPreMergeHookInDirGatesOnEnvDirNotRunDir(t *testing.T) {
	envDir := t.TempDir()
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(envDir, ".envrc"), []byte("use flake\n"), 0o644); err != nil {
		t.Fatalf("writing .envrc: %v", err)
	}

	pinDirenv(t, "#!/bin/sh\necho DIRENV_WRAPPED\nshift 2\nexec \"$@\"\n")

	sf := Sweatfile{Hooks: &Hooks{PreMerge: sptr("echo verified")}}
	var buf bytes.Buffer
	if err := sf.RunPreMergeHookInDir(context.Background(), envDir, runDir, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "DIRENV_WRAPPED") {
		t.Errorf("expected devshell wrap gated on envDir's .envrc, got %q", buf.String())
	}
}
