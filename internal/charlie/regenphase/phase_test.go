package regenphase

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(runGitOut(t, dir, args...))
}

func commitFile(t *testing.T, dir, name, body string) {
	t.Helper()
	writeFileIn(t, dir, name, body)
	gitIn(t, dir, "add", name)
	gitIn(t, dir, "commit", "-q", "-m", "commit "+name)
}

func writeFileIn(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture: a branch cut from main (flake.lock L1, gen.txt "old"), whose step
// committed a lock bump (L2) and left gen.txt stale: the situation the phase
// exists for.
type fixture struct {
	dir  string
	sha0 string // the branch's HEAD
	base string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.invalid")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	commitFile(t, dir, "flake.lock", "L1")
	commitFile(t, dir, "gen.txt", "old")
	gitIn(t, dir, "checkout", "-q", "-b", "work")
	commitFile(t, dir, "flake.lock", "L2")
	return fixture{dir: dir, sha0: gitOut(t, dir, "rev-parse", "HEAD"), base: "main"}
}

func repairSweatfile(cmd string) sweatfile.Sweatfile {
	return sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Repair: &cmd}}
}

func preCommitSweatfile(cmd string) sweatfile.Sweatfile {
	return sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreCommit: &cmd}}
}

func stubHook(t *testing.T, fn func(dir string) error) *int {
	t.Helper()
	calls := 0
	prev := runRepair
	runRepair = func(_ context.Context, _ sweatfile.Sweatfile, wt string, _ io.Writer) error {
		calls++
		return fn(wt)
	}
	t.Cleanup(func() { runRepair = prev })
	return &calls
}

// admission stands in for the lock policy and records what it saw.
type admission struct {
	refuse    error
	calls     int
	forbidden []string
}

func (a *admission) admit(_ context.Context, _ string, f []string) error {
	a.calls++
	a.forbidden = f
	return a.refuse
}

func runPhase(t *testing.T, f fixture, sf sweatfile.Sweatfile, off bool, a *admission) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	err := Phase(context.Background(), ts, sf, f.dir, f.base, off, a.admit)
	ts.Finish()
	return buf.String(), err
}

func TestPhaseIsSilentAndNeverAdmitsWhenTheLockIsUnchanged(t *testing.T) {
	f := newFixture(t)
	gitIn(t, f.dir, "reset", "-q", "--hard", "main") // HEAD back at the base: lock unchanged
	a := &admission{}
	got, err := runPhase(t, f, repairSweatfile("repair"), false, a)
	if err != nil || strings.Contains(got, "regenerate") {
		t.Errorf("an unchanged lock must add nothing and succeed: err=%v\n%s", err, got)
	}
	if a.calls != 0 {
		t.Errorf("an unchanged lock reached the lock policy %d time(s)", a.calls)
	}
}

func TestPhaseAdmitsAChangedLockBeforeTheHookRuns(t *testing.T) {
	f := newFixture(t)
	a := &admission{}
	order := ""
	stubHook(t, func(string) error {
		if a.calls != 1 {
			t.Errorf("the hook ran before the admission (calls = %d)", a.calls)
		}
		order = "hook"
		return nil
	})
	got, err := runPhase(t, f, repairSweatfile("repair"), false, a)
	if err != nil {
		t.Fatalf("an admitted lock must not fail: %v\n%s", err, got)
	}
	if order != "hook" || a.calls != 1 || len(a.forbidden) == 0 {
		t.Errorf("order=%q admission calls=%d forbidden=%v, want the hook run once after one admission naming the worktree", order, a.calls, a.forbidden)
	}
}

func TestPhaseFailsWhenThePolicyRefusesAndNeverRunsTheHook(t *testing.T) {
	f := newFixture(t)
	calls := stubHook(t, func(string) error { return nil })
	a := &admission{refuse: errors.New("the lock names an input the gate does not accept")}
	got, err := runPhase(t, f, repairSweatfile("repair"), false, a)
	if err == nil {
		t.Fatalf("a refused lock must fail the phase:\n%s", got)
	}
	if !strings.Contains(got, `"ok":false`) || !strings.Contains(got, "--no-regenerate") {
		t.Errorf("the refusal is not reported with the way out:\n%s", got)
	}
	if *calls != 0 {
		t.Error("the hook ran on a lock the policy refused")
	}
}

func TestPhaseNamesTheFlagAndNeverAdmitsWhenOff(t *testing.T) {
	f := newFixture(t)
	a := &admission{refuse: errors.New("must not be reached")}
	got, err := runPhase(t, f, repairSweatfile("repair"), true, a)
	if err != nil || !strings.Contains(got, "--no-regenerate") {
		t.Errorf("a changed lock under --no-regenerate must skip by the flag: err=%v\n%s", err, got)
	}
	if a.calls != 0 {
		t.Errorf("--no-regenerate reached the lock policy %d time(s)", a.calls)
	}
}
