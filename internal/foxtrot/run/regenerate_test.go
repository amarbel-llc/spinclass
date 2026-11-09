package run

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func runGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func commitFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", name)
	gitIn(t, dir, "commit", "-q", "-m", "commit "+name)
}

// regenerateStream runs the phase's seam on a repo whose branch `work` was cut
// from `main` (holding lock L1) and then committed lockOnWork, and returns the
// crap stream it wrote.
func regenerateStream(t *testing.T, lockOnWork string, off bool) (stream string, err error) {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	commitFile(t, dir, "flake.lock", "L1")
	gitIn(t, dir, "checkout", "-q", "-b", "work")
	if lockOnWork != "L1" {
		commitFile(t, dir, "flake.lock", lockOnWork)
	}
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	err = regeneratePhase(context.Background(), ts, sweatfile.Sweatfile{}, dir, dir, off)
	ts.Finish()
	return buf.String(), err
}

// stubAdmission replaces the lock policy for a test and records what it saw.
func stubAdmission(t *testing.T, refuse error) (calls *int, sawForbidden *[]string) {
	t.Helper()
	n := 0
	var forbidden []string
	prev := admitLock
	admitLock = func(_ context.Context, _ string, f []string) error {
		n++
		forbidden = f
		return refuse
	}
	t.Cleanup(func() { admitLock = prev })
	return &n, &forbidden
}

func TestRegeneratePhaseIsSilentAndNeverAdmitsWhenTheLockIsUnchanged(t *testing.T) {
	calls, _ := stubAdmission(t, nil)
	got, err := regenerateStream(t, "L1", false)
	if err != nil || strings.Contains(got, "regenerate") {
		t.Errorf("an unchanged lock must add nothing and succeed: err=%v\n%s", err, got)
	}
	if *calls != 0 {
		t.Errorf("an unchanged lock reached the lock policy %d time(s)", *calls)
	}
}

func TestRegeneratePhaseAdmitsAChangedLockBeforeAnythingElse(t *testing.T) {
	calls, forbidden := stubAdmission(t, nil)
	got, err := regenerateStream(t, "L2", false)
	if err != nil {
		t.Fatalf("an admitted lock must not fail the run: %v\n%s", err, got)
	}
	// No hook is configured in this fixture, so an admitted lock reaches the
	// run and skips with the reason (the run itself is tested in
	// regenerate_run_test.go).
	if !strings.Contains(got, "regenerate") || !strings.Contains(got, "nothing to run") {
		t.Errorf("an admitted lock did not reach the run:\n%s", got)
	}
	if strings.Contains(got, "--no-regenerate") {
		t.Errorf("the phase is on, so the report must not blame the flag:\n%s", got)
	}
	if *calls != 1 || len(*forbidden) == 0 {
		t.Errorf("admission calls = %d, forbidden = %v, want one call with the worktree listed", *calls, *forbidden)
	}
}

func TestRegeneratePhaseFailsTheRunWhenThePolicyRefuses(t *testing.T) {
	stubAdmission(t, errors.New("the lock names an input the gate does not accept"))
	got, err := regenerateStream(t, "L2", false)
	if err == nil {
		t.Fatalf("a refused lock must fail the phase:\n%s", got)
	}
	if !strings.Contains(got, `"ok":false`) || !strings.Contains(got, "--no-regenerate") {
		t.Errorf("the refusal is not reported with the way out:\n%s", got)
	}
}

func TestRegeneratePhaseNamesTheFlagAndNeverAdmitsWhenOff(t *testing.T) {
	calls, _ := stubAdmission(t, errors.New("must not be reached"))
	got, err := regenerateStream(t, "L2", true)
	if err != nil || !strings.Contains(got, "--no-regenerate") {
		t.Errorf("a changed lock under --no-regenerate must skip by the flag: err=%v\n%s", err, got)
	}
	if *calls != 0 {
		t.Errorf("--no-regenerate reached the lock policy %d time(s)", *calls)
	}
}

func TestParseArgsNoRegenerate(t *testing.T) {
	spec, err := ParseArgs([]string{"--no-regenerate", "--", "true"}, nil)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if !spec.NoRegenerate {
		t.Error("--no-regenerate did not set NoRegenerate")
	}
	def, err := ParseArgs([]string{"--", "true"}, nil)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if def.NoRegenerate {
		t.Error("the phase must be on by default")
	}
}
